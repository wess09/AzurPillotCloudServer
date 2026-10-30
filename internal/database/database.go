// Package database owns the shared ORM connection and versioned schema changes.
package database

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

type Connection struct {
	DB   *gorm.DB
	Pool *sql.DB
}

func Open(ctx context.Context, dsn string) (*Connection, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), TranslateError: true})
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	pool, err := db.DB()
	if err != nil {
		return nil, err
	}
	pool.SetMaxOpenConns(16)
	pool.SetMaxIdleConns(2)
	pool.SetConnMaxLifetime(30 * time.Minute)
	pool.SetConnMaxIdleTime(5 * time.Minute)
	if err := pool.PingContext(ctx); err != nil {
		_ = pool.Close()
		return nil, err
	}
	return &Connection{DB: db, Pool: pool}, nil
}

func (c *Connection) Close() error { return c.Pool.Close() }

type SchemaVersion struct {
	Component string    `gorm:"primaryKey;type:varchar(64)"`
	Version   uint      `gorm:"not null"`
	UpdatedAt time.Time `gorm:"not null"`
}

func (SchemaVersion) TableName() string { return "orm_schema_versions" }

// Migration is an append-only schema change implemented with the ORM migrator.
// Versions must be consecutive. The row lock serializes each component across instances.
type Migration struct {
	Version uint
	Apply   func(*gorm.DB) error
}

func Migrate(ctx context.Context, db *gorm.DB, component string, steps ...Migration) error {
	for i, step := range steps {
		if step.Version != uint(i+1) || step.Apply == nil {
			return fmt.Errorf("invalid %s migration %d", component, step.Version)
		}
	}
	db = db.WithContext(ctx)
	if err := db.AutoMigrate(&SchemaVersion{}); err != nil {
		return err
	}
	seed := SchemaVersion{Component: component, UpdatedAt: time.Now().UTC()}
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&seed).Error; err != nil {
		return err
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var version SchemaVersion
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&version, clause.Eq{Column: "component", Value: component}).Error; err != nil {
			return err
		}
		if version.Version > uint(len(steps)) {
			return fmt.Errorf("%s schema version %d is newer than this binary", component, version.Version)
		}
		for _, step := range steps {
			if step.Version <= version.Version {
				continue
			}
			if err := step.Apply(tx); err != nil {
				return fmt.Errorf("%s migration %d: %w", component, step.Version, err)
			}
			version.Version = step.Version
			version.UpdatedAt = time.Now().UTC()
			if err := tx.Save(&version).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
