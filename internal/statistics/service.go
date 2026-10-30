// Package statistics owns structured recognition results, template review and
// the worker management protocol. User screenshots remain in 123Pan.
package statistics

import (
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	matchSlots             chan struct{}
	signingKey             ed25519.PrivateKey
	WorkerURL, WorkerToken string
	AllowInsecureWorker    bool
	Pool                   *pgxpool.Pool
	Token, Admin           string
	FileBytes, GroupBytes  int64
	Now                    func() time.Time
}

func New(pool *pgxpool.Pool, token, admin string) *Service {
	return &Service{Pool: pool, Token: token, Admin: admin, Now: time.Now, FileBytes: 10 << 20, GroupBytes: 500 << 20, matchSlots: make(chan struct{}, 4)}
}
func (s *Service) WithLimits(file, group int64) *Service {
	s.FileBytes, s.GroupBytes = file, group
	return s
}
func (s *Service) WithWorker(url, token string) *Service {
	s.WorkerURL = strings.TrimRight(url, "/")
	s.WorkerToken = token
	return s
}
func output(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, code, message string) {
	output(w, status, map[string]any{"schema_version": 2, "error": map[string]any{"code": code, "message": message, "details": map[string]any{}}})
}
func internal(w http.ResponseWriter, e error) {
	slog.Error("statistics request", "error", e)
	fail(w, 503, "SERVICE_UNAVAILABLE", "统计服务暂不可用")
}
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		fail(w, 415, "UNSUPPORTED_MEDIA_TYPE", "需要 application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(dst); e != nil {
		var tooBig *http.MaxBytesError
		if errors.As(e, &tooBig) {
			fail(w, 413, "PAYLOAD_TOO_LARGE", "请求超过 8 MiB")
		} else {
			fail(w, 400, "VALIDATION_ERROR", e.Error())
		}
		return false
	}
	if e := d.Decode(&struct{}{}); e != io.EOF {
		fail(w, 400, "INVALID_JSON", "正文只能包含一个 JSON 对象")
		return false
	}
	return true
}
func (s *Service) auth(token string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			fail(w, 503, "SERVICE_DISABLED", "未配置服务凭据")
			return
		}
		given, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(given), []byte(token)) != 1 {
			fail(w, 401, "UNAUTHORIZED", "凭据无效")
			return
		}
		next(w, r)
	}
}
func (s *Service) Register(m *http.ServeMux) {
	m.HandleFunc("/api/data/opsi_items", legacyPublic(s.legacyOpsiItems))
	m.HandleFunc("GET /api/v1/admin/statistics/preview/{sha256}/{index}", s.auth(s.Admin, s.preview))
	m.HandleFunc("POST /internal/v1/statistics/results", s.auth(s.Token, s.ingest))
	m.HandleFunc("GET /internal/v1/statistics/results/{sha256}", s.auth(s.Token, s.receipt))
	m.HandleFunc("POST /internal/v1/template-candidates", s.auth(s.Token, s.candidate))
	m.HandleFunc("POST /internal/v1/unknown-screenshots", s.auth(s.Token, s.screenshotReference))
	m.HandleFunc("GET /api/v3/get/templates/manifest", s.manifest)
	m.HandleFunc("GET /api/v3/get/templates/delta", s.delta)
	m.HandleFunc("GET /api/v3/get/mappings", s.delta)
	m.HandleFunc("GET /api/v1/admin/unknown-clusters", s.auth(s.Admin, s.clusters))
	m.HandleFunc("POST /api/v1/admin/unknown-clusters/{action}", s.auth(s.Admin, s.clusterEdit))
	m.HandleFunc("POST /internal/v1/worker/sync", s.auth(s.Token, s.syncWorker))
	m.HandleFunc("GET /internal/v1/template-releases/{id}", s.auth(s.Token, s.release))
	m.HandleFunc("GET /api/v2/get/research", s.summary)
	m.HandleFunc("GET /api/v2/get/research/options", s.summary)
	m.HandleFunc("GET /api/v2/get/research/time-slots", s.summary)
	m.HandleFunc("GET /api/v2/get/drop", s.summary)
	m.HandleFunc("GET /api/v2/get/drop/options", s.summary)
	m.HandleFunc("GET /api/v2/get/drop/time-slots", s.summary)
	for _, route := range []string{"GET /api/v1/admin/statistics/overview", "GET /api/v1/admin/statistics/results", "GET /api/v1/admin/template-candidates", "GET /api/v1/admin/templates", "GET /api/v1/admin/template-releases", "GET /api/v1/admin/worker-commands"} {
		m.HandleFunc(route, s.auth(s.Admin, s.adminList))
	}
	m.HandleFunc("PATCH /api/v1/admin/template-candidates/{id}", s.auth(s.Admin, s.classify))
	m.HandleFunc("POST /api/v1/admin/templates/publish", s.auth(s.Admin, s.publish))
	m.HandleFunc("POST /api/v1/admin/templates/rollback", s.auth(s.Admin, s.rollback))
	m.HandleFunc("POST /api/v1/admin/templates/test", s.auth(s.Admin, s.testTemplate))
	m.HandleFunc("POST /api/v1/admin/statistics/reprocess", s.auth(s.Admin, s.reprocess))
	m.HandleFunc("POST /api/v1/admin/statistics/reprocess-unresolved", s.auth(s.Admin, s.reprocessUnresolved))
}
