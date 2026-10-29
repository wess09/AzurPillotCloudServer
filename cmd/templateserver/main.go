// Command templateserver 启动独立部署的模板管理与发布服务。
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"ap-cloud-server/internal/templateserver"
	"ap-cloud-server/internal/v3db"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	if err := run(logger); err != nil {
		logger.Error("模板服务器退出", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := templateserver.LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := v3db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	signer, err := loadSigner(cfg, logger)
	if err != nil {
		return err
	}

	srv, err := templateserver.NewServer(cfg, store.DB, signer, logger)
	if err != nil {
		return err
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	logger.Info("模板服务器已启动",
		"addr", cfg.ListenAddr,
		"public_base", cfg.PublicBase,
		"cf_deploy", cfg.CFDeploy,
		"public_key", signer.PublicKeyBase64(),
	)

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve: %w", err)
		}
	case <-ctx.Done():
		logger.Info("收到停机信号")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownGrace)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func loadSigner(cfg templateserver.Config, logger *slog.Logger) (*templateserver.Signer, error) {
	if cfg.SigningKey != "" {
		return templateserver.NewSigner(cfg.SigningKey)
	}
	signer, err := templateserver.GenerateSigner()
	if err != nil {
		return nil, fmt.Errorf("generate signing key: %w", err)
	}
	logger.Warn("未配置 TEMPLATE_SIGNING_KEY，已生成临时密钥；重启后签名会变化，客户端固定公钥将失效",
		"public_key", signer.PublicKeyBase64())
	return signer, nil
}
