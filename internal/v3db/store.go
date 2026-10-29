package v3db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// Store 封装 GORM 句柄与底层 database/sql 连接池。
type Store struct {
	DB  *gorm.DB
	SQL *sql.DB
}

// Open 建立连接池并执行迁移。连接池参数与主服务的 pgxpool 保持一致，
// 避免两套访问路径给出不同的超时行为。
func Open(ctx context.Context, dsn string) (*Store, error) {
	gdb, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return nil, fmt.Errorf("open v3 database: %w", err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return nil, fmt.Errorf("v3 database pool: %w", err)
	}
	sqlDB.SetMaxOpenConns(16)
	sqlDB.SetMaxIdleConns(2)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)
	sqlDB.SetConnMaxIdleTime(5 * time.Minute)
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping v3 database: %w", err)
	}
	s := &Store{DB: gdb, SQL: sqlDB}
	if err := s.migrate(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	if s == nil || s.SQL == nil {
		return nil
	}
	return s.SQL.Close()
}

// migrate 先建 guard 表，再用单例行锁把多个实例的 AutoMigrate 串行化。
func (s *Store) migrate(ctx context.Context) error {
	if err := s.DB.WithContext(ctx).AutoMigrate(&SchemaGuard{}); err != nil {
		return fmt.Errorf("create schema guard: %w", err)
	}
	guard := SchemaGuard{ID: 1, MigratedAt: time.Now().UTC()}
	if err := s.DB.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&guard).Error; err != nil {
		return fmt.Errorf("seed schema guard: %w", err)
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row SchemaGuard
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", 1).Take(&row).Error; err != nil {
			return fmt.Errorf("lock schema guard: %w", err)
		}
		if err := tx.AutoMigrate(AllModels()...); err != nil {
			return fmt.Errorf("migrate v3 schema: %w", err)
		}
		return tx.Model(&SchemaGuard{}).Where("id = ?", 1).Update("migrated_at", time.Now().UTC()).Error
	})
}

// MarkDirtyHour 在调用方事务内登记一个待重算的 UTC 小时。
func MarkDirtyHour(tx *gorm.DB, hour time.Time) error {
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&DirtyHour{
		Hour:      hour.UTC().Truncate(time.Hour),
		CreatedAt: time.Now().UTC(),
	}).Error
}

// EnsureState 保证模板服务器单例状态行存在。
func EnsureState(ctx context.Context, db *gorm.DB, publicBase string) (*TemplateState, error) {
	state := TemplateState{ID: 1, PublicBase: publicBase}
	if err := db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&state).Error; err != nil {
		return nil, err
	}
	var out TemplateState
	if err := db.WithContext(ctx).Where("id = ?", 1).Take(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}
