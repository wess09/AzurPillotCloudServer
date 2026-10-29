package templateserver

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var workerNameRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,63}$`)

// Config 是模板服务器的运行配置。
type Config struct {
	ListenAddr  string
	DatabaseURL string
	AdminToken  string
	// IngestToken 用于主 API 转发客户端未知模板上传；为空时关闭上传端点。
	IngestToken   string
	SigningKey    string
	PublishDir    string
	PublicBase    string
	WorkerName    string
	WranglerImage string
	CFDeploy      bool
	CFAccountID   string
	CFAPIToken    string
	SelfCheck     bool
	CheckAttempts int
	CheckDelay    time.Duration
	ExpiresDays   int
	ShutdownGrace time.Duration
}

// LoadConfig 从环境变量读取模板服务器配置。
func LoadConfig() (Config, error) {
	cfg := Config{
		ListenAddr:    env("LISTEN_ADDR", ":8090"),
		DatabaseURL:   env("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/apcloud?sslmode=disable"),
		AdminToken:    env("ADMIN_TOKEN", ""),
		IngestToken:   env("TEMPLATE_INGEST_TOKEN", ""),
		SigningKey:    env("TEMPLATE_SIGNING_KEY", ""),
		PublishDir:    env("TEMPLATE_PUBLISH_DIR", "templates-public"),
		PublicBase:    env("TEMPLATE_PUBLIC_BASE", "https://templates.azurpilot.com"),
		WorkerName:    env("CF_WORKER_NAME", "azurpilot-templates"),
		WranglerImage: envRaw("TEMPLATE_WRANGLER_IMAGE", "azurpilot-wrangler:4"),
		CFDeploy:      env("CF_DEPLOY", "false") == "true",
		CFAccountID:   env("CLOUDFLARE_ACCOUNT_ID", env("CF_ACCOUNT_ID", "")),
		CFAPIToken:    env("CLOUDFLARE_API_TOKEN", env("CF_API_TOKEN", "")),
		SelfCheck:     env("TEMPLATE_SELF_CHECK", "true") != "false",
		CheckAttempts: envInt("TEMPLATE_SELF_CHECK_ATTEMPTS", 20),
		CheckDelay:    envDuration("TEMPLATE_SELF_CHECK_DELAY", 5*time.Second),
		ExpiresDays:   envInt("TEMPLATE_EXPIRES_DAYS", 30),
		ShutdownGrace: envDuration("SHUTDOWN_GRACE", 30*time.Second),
	}
	if strings.TrimSpace(cfg.DatabaseURL) == "" {
		return cfg, fmt.Errorf("DATABASE_URL 不能为空")
	}
	if strings.TrimSpace(cfg.PublishDir) == "" {
		return cfg, fmt.Errorf("TEMPLATE_PUBLISH_DIR 不能为空")
	}
	if cfg.CFDeploy && (cfg.CFAccountID == "" || cfg.CFAPIToken == "") {
		return cfg, fmt.Errorf("CF_DEPLOY=true 需要 CLOUDFLARE_ACCOUNT_ID 与 CLOUDFLARE_API_TOKEN")
	}
	if cfg.CFDeploy && publicHost(cfg.PublicBase) == "" {
		return cfg, fmt.Errorf("CF_DEPLOY=true 需要有效的 TEMPLATE_PUBLIC_BASE")
	}
	if cfg.ExpiresDays < 1 || cfg.ExpiresDays > 365 {
		return cfg, fmt.Errorf("TEMPLATE_EXPIRES_DAYS 需在 1–365 之间")
	}
	if !workerNameRE.MatchString(cfg.WorkerName) {
		return cfg, fmt.Errorf("CF_WORKER_NAME 只能包含字母、数字、下划线和连字符，长度 1–63")
	}
	if cfg.CheckAttempts < 1 || cfg.CheckAttempts > 120 {
		return cfg, fmt.Errorf("TEMPLATE_SELF_CHECK_ATTEMPTS 需在 1–120 之间")
	}
	if cfg.CheckDelay <= 0 || cfg.CheckDelay > time.Minute {
		return cfg, fmt.Errorf("TEMPLATE_SELF_CHECK_DELAY 需在 0–1m 之间")
	}
	return cfg, nil
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return fallback
}

// envRaw 保留显式空值，用于“置空即关闭”的开关（例如 TEMPLATE_WRANGLER_IMAGE）。
func envRaw(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return strings.TrimSpace(v)
	}
	return fallback
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
