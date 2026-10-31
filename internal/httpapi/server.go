package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"ap-cloud-server/internal/adminws"
	"ap-cloud-server/internal/config"
	"ap-cloud-server/internal/drops"
	"ap-cloud-server/internal/maintenance"
	"ap-cloud-server/internal/publish"
	"ap-cloud-server/internal/statuscache"
	"ap-cloud-server/internal/store"
	"ap-cloud-server/internal/templatesync"
)

// Backend 是 HTTP 层需要的存储接口；*store.Store 天然满足，
// 测试可用假实现替换。
type Backend interface {
	SubmitCl1(ctx context.Context, rec store.Cl1Record, bodyHash string, responseJSON []byte) (store.Cl1Outcome, []byte, error)
	LatestMaintenance(ctx context.Context, server string) (*store.Maintenance, error)
	UpsertMaintenance(ctx context.Context, m store.Maintenance) error
	DeleteMaintenance(ctx context.Context, server string) (bool, error)
	ListMaintenances(ctx context.Context) ([]store.Maintenance, error)
	LatestAnnouncement(ctx context.Context) (*store.Announcement, error)
	GenerateAnnouncementID(ctx context.Context, now time.Time) (string, error)
	SetAnnouncement(ctx context.Context, a store.Announcement) error
	WithdrawAnnouncement(ctx context.Context) error
	ListAnnouncements(ctx context.Context, limit int) ([]store.Announcement, error)
	GetUpdatePolicy(ctx context.Context) (bool, error)
	SetUpdatePolicy(ctx context.Context, force bool) error
	RecordPublish(ctx context.Context, rec store.PublishRecord) error
	LatestPublishByWorker(ctx context.Context) (map[string]store.PublishRecord, error)
}

// Deps 是 HTTP 服务依赖。
type MaintenanceSync interface {
	Status(context.Context) (maintenance.Status, error)
	Sync(context.Context) error
}

type Deps struct {
	Maintenance MaintenanceSync
	// Drops 为 nil 时 v3 上报与查询接口返回 503。
	Drops        *drops.Service
	TemplateSync *templatesync.Service
	Cfg          config.Config
	Store        Backend
	Cache        *statuscache.Cache
	Pub          *publish.Publisher
	Logger       *slog.Logger
	// Now 可注入时钟，测试用；nil 时使用 time.Now。
	Now func() time.Time
}

// Server 是装配完成的 HTTP 服务。
type Server struct {
	http        *http.Server
	adminSocket *adminws.Server
	deps        Deps

	store     Backend
	cache     *statuscache.Cache
	publisher *publish.Publisher
	logger    *slog.Logger

	adminToken      string
	publishDir      string
	noticePublicURL string
	updatePublicURL string
	publishTimeout  time.Duration
	requestTimeout  time.Duration
	limiter         *rateLimiter
	now             func() time.Time
}

// New 装配路由与中间件，返回可启动的 Server。
func New(deps Deps) *Server {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	s := &Server{
		deps:            deps,
		store:           deps.Store,
		cache:           deps.Cache,
		publisher:       deps.Pub,
		logger:          deps.Logger,
		adminToken:      deps.Cfg.AdminToken,
		publishDir:      deps.Cfg.PublishDir,
		noticePublicURL: deps.Cfg.Cloudflare.NoticePublicURL,
		updatePublicURL: deps.Cfg.Cloudflare.UpdatePublicURL,
		publishTimeout:  deps.Cfg.PublishTimeout,
		requestTimeout:  deps.Cfg.RequestTimeout,
		limiter:         newRateLimiter(deps.Cfg.RateLimitRPS, deps.Cfg.RateLimitBurst),
		now:             deps.Now,
	}

	s.adminSocket = adminws.New(deps.Cfg.AdminToken, deps.Cfg.AdminOrigins, s.executeAdmin, s.adminSnapshot)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("POST /internal/v1/templates/events", s.handleTemplateEvent)
	// v2 动态接口：方法在处理器内部校验，错误方法返回统一 405 信封。
	mux.HandleFunc("/api/v2/get/server", requireDeviceID(s.handleGetServer))
	mux.HandleFunc("/api/v2/get/maintenance", requireDeviceID(s.handleGetMaintenance))
	mux.HandleFunc("/api/v2/post/cl1", requireDeviceID(s.handlePostCl1))
	mux.HandleFunc("/api/telemetry", legacyPublic(s.handleLegacyTelemetry))
	mux.HandleFunc("/api/get/announcement", legacyPublic(s.handleLegacyAnnouncement))
	mux.HandleFunc("/api/force_update", legacyPublic(s.handleLegacyForceUpdate))
	mux.HandleFunc("/api/v1/servers/{id}", legacyPublic(s.handleLegacyServer))
	mux.HandleFunc("/api/maintenance", legacyPublic(s.handleLegacyMaintenance))
	// v3 委托/科研统计。
	mux.HandleFunc("POST /api/v3/post/drops", s.handleV3Drops)
	mux.HandleFunc("GET /api/v3/get/commission", s.handleV3Commission)
	mux.HandleFunc("GET /api/v3/get/commission/options", s.handleV3CommissionOptions)
	mux.HandleFunc("GET /api/v3/get/research", s.handleV3Research)
	mux.HandleFunc("GET /api/v3/get/research/options", s.handleV3ResearchOptions)
	mux.HandleFunc("GET /api/v3/get/analysis", s.handleV3Analysis)
	mux.HandleFunc("GET /api/v3/get/items", s.handleV3Items)
	mux.HandleFunc("GET /api/v3/get/cl1", s.handleV3Cl1Statistics)
	mux.Handle("GET /api/v1/admin/ws", s.adminSocket)
	// 兜底：统一 404 错误信封。
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, apiError{
			status:  http.StatusNotFound,
			code:    "NOT_FOUND",
			message: "请求的路径不存在",
			details: map[string]any{},
		})
	})

	handler := recoverMiddleware(s.logger, mux)
	handler = s.rateLimitMiddleware(handler)
	handler = logMiddleware(s.logger, handler)
	handler = timeoutMiddleware(s.requestTimeout, s.publishTimeout, handler)
	handler = publicCORSMiddleware(deps.Cfg.PublicOrigins, handler)

	s.http = &http.Server{
		Addr:              deps.Cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	return s
}

// Run 启动附带的内部协程（限流器空闲回收）。
func (s *Server) Run(ctx context.Context) {
	s.limiter.Run(ctx)
	s.adminSocket.Run(ctx)
}

// Close 停止内部协程。
func (s *Server) Close() {
	s.adminSocket.Close()
	s.limiter.Close()
}

// Handler 返回装配后的根处理器，供测试直接驱动。
func (s *Server) Handler() http.Handler {
	return s.http.Handler
}

// ListenAndServe 启动 HTTP 监听。
func (s *Server) ListenAndServe() error {
	return s.http.ListenAndServe()
}

// Shutdown 优雅停机：停止接收新请求并等待存量请求完成。
func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}

// handleHealthz 提供存活探针。
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, r, errMethodNotAllowed("GET, HEAD"))
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"status": "ok"})
}
