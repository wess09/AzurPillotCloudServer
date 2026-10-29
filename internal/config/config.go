// Package config 从环境变量加载服务配置。
//
// 所有配置项都有可用默认值，仅 DATABASE_URL 在生产环境必须显式提供
// （开发环境默认连接本机 PostgreSQL）。
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Cloudflare 保存公告与更新策略两个静态 Worker 的发布凭据与地址。
type Cloudflare struct {
	// AccountID 为 Cloudflare 账户 ID；为空时发布功能整体禁用，
	// 管理端仍会生成本地静态文件。
	AccountID string
	// APIToken 为具有 Workers Scripts 编辑权限的 API Token。
	// 凭据只保存在服务器端，任何客户端接口不得回显。
	APIToken string
	// NoticeWorker / UpdateWorker 为两个 Worker 的脚本名。
	NoticeWorker string
	UpdateWorker string
	// NoticePublicURL / UpdatePublicURL 为发布后的公网读取地址，
	// 发布器用它做读取确认。
	NoticePublicURL string
	UpdatePublicURL string
	// Timeout 为单次 Cloudflare API 调用超时。
	Timeout time.Duration
}

// Config 是服务的全部运行配置。
type Config struct {
	ListenAddr string
	// DatabaseURL 为 PostgreSQL 连接串（libpq 格式）。
	DatabaseURL string
	// AdminToken 为管理接口的 Bearer Token；为空时管理接口整体关闭。
	AdminToken string
	// StatisticsToken authenticates B's result, candidate and control requests.
	StatisticsToken      string
	RecognizerURL        string
	WorkerToken          string
	StatisticsFileBytes  int64
	StatisticsGroupBytes int64
	LegacyUploadURL      string
	// LogLevel 为 slog 级别：debug、info、warn、error。
	LogLevel string

	// RequestTimeout 为单个 HTTP 请求的处理上限（含数据库访问）。
	RequestTimeout time.Duration
	// ShutdownGrace 为优雅停机的最长等待时间。
	ShutdownGrace time.Duration

	// RateLimitRPS 与 RateLimitBurst 为按来源 IP、请求类别独立的令牌桶参数。
	RateLimitRPS   float64
	RateLimitBurst int

	// GatewayTimeout 为单次区服网关查询超时。
	GatewayTimeout time.Duration
	// CacheTTL 为区服状态缓存的有效期。
	CacheTTL time.Duration
	// CacheRefreshInterval 为后台保活刷新间隔。
	CacheRefreshInterval time.Duration

	// DedupTTL 为 CL1 请求去重结果的保留时长（计划规定 24 小时）。
	DedupTTL time.Duration
	// JanitorInterval 为后台清理协程的运行间隔。
	JanitorInterval time.Duration

	// PublishDir 为公告 / 更新策略静态文件的本地生成目录。
	PublishDir string
	// PublishTimeout 为单次发布的整体超时。
	PublishTimeout          time.Duration
	MaintenanceSyncEnabled  bool
	MaintenanceSyncInterval time.Duration
	MaintenanceSyncTimeout  time.Duration

	// V3Enabled 开启委托/科研 v3 上报、聚合与查询。
	V3Enabled bool
	// AggInterval 为 v3 小时聚合任务的运行间隔。
	AggInterval time.Duration
	// TemplatePublicBase 为模板 manifest/delta/mappings 的上游地址。
	// 为空时模板读取接口返回 503。
	TemplatePublicBase string
	// TemplateProxyCache 为模板读取缓存时长。
	TemplateProxyCache time.Duration
	// TemplateIngestURL 为模板服务器未知模板上传端点；为空时上传接口返回 503。
	TemplateIngestURL string
	// TemplateIngestToken 是调用模板服务器的 Bearer 凭据。
	TemplateIngestToken string
	TemplateAdminURL    string
	TemplateAdminToken  string
	TemplateEventToken  string

	Cloudflare Cloudflare
}

// Load 从环境变量读取配置并填充默认值。
func Load() (Config, error) {
	cfg := Config{
		ListenAddr:              env("LISTEN_ADDR", ":8080"),
		DatabaseURL:             env("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/apcloud?sslmode=disable"),
		AdminToken:              env("ADMIN_TOKEN", ""),
		StatisticsToken:         env("STATISTICS_TOKEN", ""),
		LegacyUploadURL:         env("LEGACY_UPLOAD_PUBLIC_URL", ""),
		RecognizerURL:           env("RECOGNIZER_URL", ""),
		WorkerToken:             env("WORKER_TOKEN", ""),
		StatisticsFileBytes:     int64(envInt("MAX_FILE_BYTES", 10<<20)),
		StatisticsGroupBytes:    int64(envInt("MAX_GROUP_BYTES", 500<<20)),
		LogLevel:                env("LOG_LEVEL", "info"),
		RequestTimeout:          envDuration("REQUEST_TIMEOUT", 15*time.Second),
		ShutdownGrace:           envDuration("SHUTDOWN_GRACE", 30*time.Second),
		RateLimitRPS:            envFloat("RATE_LIMIT_RPS", 5000),
		RateLimitBurst:          envInt("RATE_LIMIT_BURST", 8000),
		GatewayTimeout:          envDuration("GATEWAY_TIMEOUT", 10*time.Second),
		CacheTTL:                envDuration("CACHE_TTL", 8*time.Second),
		CacheRefreshInterval:    envDuration("CACHE_REFRESH_INTERVAL", 5*time.Second),
		DedupTTL:                envDuration("DEDUP_TTL", 24*time.Hour),
		JanitorInterval:         envDuration("JANITOR_INTERVAL", time.Hour),
		PublishDir:              env("PUBLISH_DIR", "public"),
		PublishTimeout:          envDuration("PUBLISH_TIMEOUT", 90*time.Second),
		MaintenanceSyncEnabled:  env("MAINTENANCE_SYNC_ENABLED", "true") != "false",
		MaintenanceSyncInterval: envDuration("MAINTENANCE_SYNC_INTERVAL", 10*time.Minute),
		MaintenanceSyncTimeout:  envDuration("MAINTENANCE_SYNC_TIMEOUT", 12*time.Second),
		V3Enabled:               env("V3_ENABLED", "true") != "false",
		AggInterval:             envDuration("AGG_INTERVAL", time.Hour),
		TemplatePublicBase:      env("TEMPLATE_PUBLIC_BASE", "https://templates.azurpilot.com"),
		TemplateProxyCache:      envDuration("TEMPLATE_PROXY_CACHE", 60*time.Second),
		TemplateIngestURL:       env("TEMPLATE_INGEST_URL", ""),
		TemplateIngestToken:     env("TEMPLATE_INGEST_TOKEN", ""),
		TemplateAdminURL:        env("TEMPLATE_ADMIN_URL", ""),
		TemplateAdminToken:      env("TEMPLATE_ADMIN_TOKEN", ""),
		TemplateEventToken:      env("TEMPLATE_EVENT_TOKEN", ""),
	}
	cfg.Cloudflare = Cloudflare{
		AccountID:       env("CF_ACCOUNT_ID", ""),
		APIToken:        env("CF_API_TOKEN", ""),
		NoticeWorker:    env("CF_NOTICE_WORKER", "ap-notice"),
		UpdateWorker:    env("CF_UPDATE_WORKER", "ap-update-policy"),
		NoticePublicURL: env("CF_NOTICE_PUBLIC_URL", ""),
		UpdatePublicURL: env("CF_UPDATE_PUBLIC_URL", ""),
		Timeout:         envDuration("CF_TIMEOUT", 30*time.Second),
	}

	if strings.TrimSpace(cfg.DatabaseURL) == "" {
		return cfg, fmt.Errorf("DATABASE_URL 不能为空")
	}
	if cfg.RateLimitRPS <= 0 || cfg.RateLimitBurst <= 0 {
		return cfg, fmt.Errorf("RATE_LIMIT_RPS 和 RATE_LIMIT_BURST 必须为正数")
	}
	if cfg.CacheRefreshInterval > cfg.CacheTTL {
		// 保活间隔大于 TTL 会让缓存永远靠请求触发刷新，收紧到 TTL 以内。
		cfg.CacheRefreshInterval = cfg.CacheTTL
	}
	if cfg.StatisticsFileBytes < 8 || cfg.StatisticsGroupBytes < cfg.StatisticsFileBytes {
		return cfg, fmt.Errorf("invalid statistics file/group limits")
	}
	if cfg.MaintenanceSyncInterval < time.Minute || cfg.MaintenanceSyncTimeout <= 0 || cfg.MaintenanceSyncTimeout > 12*time.Second {
		return cfg, fmt.Errorf("维护同步间隔至少 1m，超时需在 0–12s 之间")
	}
	if cfg.AggInterval < time.Minute {
		return cfg, fmt.Errorf("AGG_INTERVAL 至少为 1m")
	}
	if cfg.TemplateProxyCache <= 0 || cfg.TemplateProxyCache > time.Hour {
		return cfg, fmt.Errorf("TEMPLATE_PROXY_CACHE 需在 1s–1h 之间")
	}
	return cfg, nil
}

// CloudflareEnabled 报告是否具备发布到 Cloudflare 的凭据。
func (c Config) CloudflareEnabled() bool {
	return c.Cloudflare.AccountID != "" && c.Cloudflare.APIToken != ""
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v := env(key, "")
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}

func envInt(key string, fallback int) int {
	v := env(key, "")
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func envFloat(key string, fallback float64) float64 {
	v := env(key, "")
	if v == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return fallback
	}
	return f
}
