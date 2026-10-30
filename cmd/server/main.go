// ap-cloud-server 是 AzurPilot 云 API v2 的自建服务端。
//
// 职责（重构计划）：
//   - GET /api/v2/get/server 与 /api/v2/get/maintenance（区服状态与维护计划）
//   - POST /api/v2/post/cl1（月度快照，带去重与冲突检测）
//   - 公告与更新策略静态文件的生成与 Cloudflare Workers 发布
//
// 启动顺序：数据库迁移 → 状态缓存后台刷新 → 发布队列 → HTTP 服务；
// 停机顺序：HTTP 优雅停机 → 取消后台 ctx → 等待全部协程退出 → 关闭连接池。
// 所有协程都受 WaitGroup 管控，正常停机不泄露。
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ap-cloud-server/internal/config"
	"ap-cloud-server/internal/drops"
	"ap-cloud-server/internal/gateway"
	"ap-cloud-server/internal/httpapi"
	"ap-cloud-server/internal/maintenance"
	"ap-cloud-server/internal/publish"
	"ap-cloud-server/internal/statuscache"
	"ap-cloud-server/internal/store"
	"ap-cloud-server/internal/templatesync"
	"ap-cloud-server/internal/v3db"
	"ap-cloud-server/internal/version"
)

func main() {
	showVersion := flag.Bool("version", false, "打印版本后退出")
	flag.Parse()
	if *showVersion {
		println(version.Version)
		return
	}

	if err := run(); err != nil {
		slog.Error("服务异常退出", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := newLogger(cfg.LogLevel)
	logger.Info("服务启动", "version", version.Version, "addr", cfg.ListenAddr)

	// 停机信号：一次取消开始优雅停机，第二次直接退出。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 1. 数据库。
	st, err := store.Open(ctx, cfg.DatabaseURL, cfg.DedupTTL)
	if err != nil {
		return err
	}
	defer func() {
		if err := st.Close(); err != nil {
			logger.Warn("关闭数据库连接池失败", "err", err)
		}
	}()
	logger.Info("数据库连接就绪")

	// 1.1 委托/科研统计与核心业务共用一个 ORM 连接池。
	var dropsSvc *drops.Service
	var templateSync *templatesync.Service
	templateWake := make(chan struct{}, 1)
	if cfg.V3Enabled {
		if err := v3db.Migrate(ctx, st.DB); err != nil {
			return err
		}
		dropsSvc = drops.NewService(st.DB)
		templateSync = templatesync.New(st.DB, templateWake)
		aggCtx, aggCancel := context.WithCancel(context.WithoutCancel(ctx))
		aggDone := make(chan struct{})
		go func() {
			defer close(aggDone)
			drops.NewAggregator(st.DB, cfg.AggInterval, logger).WithWake(templateWake).Run(aggCtx)
		}()
		defer func() {
			aggCancel()
			<-aggDone
		}()
		logger.Info("v3 委托/科研统计已启用", "agg_interval", cfg.AggInterval)
	} else {
		logger.Warn("V3_ENABLED=false，委托/科研接口返回 503")
	}

	// 2. 区服状态缓存（后台保活）。
	cache := statuscache.New(
		cfg.CacheTTL, cfg.CacheRefreshInterval, cfg.GatewayTimeout,
		func(ctx context.Context, region string) ([]gateway.RawServer, error) {
			return gateway.QueryRegion(ctx, region, cfg.GatewayTimeout)
		},
		logger,
	)
	cache.Run(ctx)
	defer cache.Close()

	// 3. 发布队列（每个 Worker 串行）。
	var cfClient *publish.Client
	if cfg.CloudflareEnabled() {
		cfClient = publish.NewClient(
			cfg.Cloudflare.AccountID, cfg.Cloudflare.APIToken,
			cfg.Cloudflare.Timeout, "https://api.cloudflare.com")
		logger.Info("Cloudflare 发布已启用", "account", cfg.Cloudflare.AccountID)
	} else {
		logger.Warn("未配置 Cloudflare 凭据，公告与更新策略只生成到本地目录", "dir", cfg.PublishDir)
	}
	publisher := publish.New(cfClient, st, cfg.PublishDir, logger)
	publisher.Run(ctx)
	defer publisher.Close()

	maintenanceSync := maintenance.New(st, cfg.MaintenanceSyncEnabled,
		cfg.MaintenanceSyncInterval, cfg.MaintenanceSyncTimeout, logger)
	maintenanceSync.Run(ctx)
	defer maintenanceSync.Close()

	// 4. 清理协程（去重记录过期等）。
	cleanupCtx, cleanupCancel := context.WithCancel(context.WithoutCancel(ctx))
	janitorDone := make(chan struct{})
	go func() {
		defer close(janitorDone)
		runJanitor(cleanupCtx, st, cfg.JanitorInterval, logger)
	}()
	defer func() {
		cleanupCancel()
		<-janitorDone
	}()

	// 5. HTTP 服务。
	srv := httpapi.New(httpapi.Deps{
		Maintenance:  maintenanceSync,
		Drops:        dropsSvc,
		TemplateSync: templateSync,
		Cfg:          cfg,
		Store:        st,
		Cache:        cache,
		Pub:          publisher,
		Logger:       logger,
	})
	srv.Run(ctx)
	defer srv.Close()

	serveErr := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()
	logger.Info("HTTP 服务就绪", "listen", cfg.ListenAddr)

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	// 优雅停机：给存量请求一个宽限期。
	logger.Info("收到停机信号，开始优雅停机")
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.ShutdownGrace)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("HTTP 停机超时", "err", err)
	}
	return nil
}

// runJanitor 周期清理过期数据；ctx 取消后退出。
func runJanitor(ctx context.Context, st *store.Store, interval time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := st.CleanupExpired(ctx); err != nil {
				logger.Warn("过期数据清理失败", "err", err)
			}
		}
	}
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	return slog.New(handler)
}
