// Package store 封装全部 PostgreSQL 访问：连接池、内嵌迁移与各业务表操作。
//
// 所有查询都通过 ctx 控制取消；连接池由调用方在优雅停机时关闭。
package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store 是数据库访问入口。
type Store struct {
	Pool *pgxpool.Pool
	// DedupTTL 为 CL1 去重记录保留时长。
	DedupTTL time.Duration
}

// Open 建立连接池并执行内嵌迁移。
func Open(ctx context.Context, databaseURL string, dedupTTL time.Duration) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("解析 DATABASE_URL 失败: %w", err)
	}
	// 合理的连接池上限，避免突发流量拖垮 PG。
	cfg.MaxConns = 16
	cfg.MinConns = 2
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = time.Minute
	// Expanding the review/mapping views can trigger very expensive LLVM JIT
	// compilation even for small summaries. Prefer the normal executor; an
	// operator can explicitly opt in with jit=on in the connection string.
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	if _, explicit := cfg.ConnConfig.RuntimeParams["jit"]; !explicit {
		cfg.ConnConfig.RuntimeParams["jit"] = "off"
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("创建连接池失败: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("数据库 ping 失败: %w", err)
	}

	s := &Store{Pool: pool, DedupTTL: dedupTTL}
	if err := s.migrate(pingCtx); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

// Close 关闭连接池。
func (s *Store) Close() {
	if s.Pool != nil {
		s.Pool.Close()
	}
}

// migrate 依序执行未应用的迁移，每个迁移在独立事务中提交。
func (s *Store) migrate(ctx context.Context) error {
	// Different server instances (and parallel test packages) may open the same
	// fresh database. Lock before creating even the migration journal; individual
	// schema versions still commit separately for restart-safe upgrades.
	guard, err := s.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("建立迁移锁事务失败: %w", err)
	}
	defer func() { _ = guard.Rollback(ctx) }()
	if _, err = guard.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('ap-cloud-server/schema-migrations',0))`); err != nil {
		return fmt.Errorf("获取迁移锁失败: %w", err)
	}
	if _, err := s.Pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("创建 schema_migrations 失败: %w", err)
	}

	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("读取内嵌迁移失败: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		var version int
		if _, err := fmt.Sscanf(name, "%d_", &version); err != nil {
			return fmt.Errorf("迁移文件名 %q 无法解析版本号", name)
		}
		var exists bool
		if err := s.Pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, version,
		).Scan(&exists); err != nil {
			return fmt.Errorf("查询迁移状态失败: %w", err)
		}
		if exists {
			continue
		}
		sqlBytes, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("读取迁移 %q 失败: %w", name, err)
		}
		tx, err := s.Pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("开启迁移事务失败: %w", err)
		}
		if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("应用迁移 %q 失败: %w", name, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("记录迁移 %q 失败: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("提交迁移 %q 失败: %w", name, err)
		}
	}
	return guard.Commit(ctx)
}

// CleanupExpired 删除过期的去重记录与过旧的发布历史。
// 由后台清理协程周期调用。
func (s *Store) CleanupExpired(ctx context.Context) error {
	if _, err := s.Pool.Exec(ctx, `DELETE FROM unknown_screenshots WHERE expires_at<=now()`); err != nil {
		return fmt.Errorf("清理过期原图引用失败: %w", err)
	}
	if _, err := s.Pool.Exec(ctx, `DELETE FROM cl1_dedup WHERE expires_at < now()`); err != nil {
		return fmt.Errorf("清理去重记录失败: %w", err)
	}
	if _, err := s.Pool.Exec(ctx,
		`DELETE FROM publish_history WHERE created_at < now() - interval '30 days'`); err != nil {
		return fmt.Errorf("清理发布历史失败: %w", err)
	}
	return nil
}
