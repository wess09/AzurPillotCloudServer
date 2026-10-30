// Package store implements core business persistence through ORM models.
package store

import (
	"context"
	"time"

	"ap-cloud-server/internal/database"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Store struct {
	*database.Connection
	DedupTTL time.Duration
}

func Open(ctx context.Context, dsn string, dedupTTL time.Duration) (*Store, error) {
	connection, err := database.Open(ctx, dsn)
	if err != nil {
		return nil, err
	}
	s := &Store{Connection: connection, DedupTTL: dedupTTL}
	if err := database.Migrate(ctx, s.DB, "core",
		database.Migration{Version: 1, Apply: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(coreModels()...); err != nil {
				return err
			}
			return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&UpdatePolicy{ID: true}).Error
		}},
		database.Migration{Version: 2, Apply: func(tx *gorm.DB) error {
			// Retire only the explicitly abandoned A/B and embedded-template schema.
			return tx.Migrator().DropTable("candidate_sources", "stat_templates", "template_candidates", "stat_items", "stat_events", "stat_result_versions", "stat_results", "template_active", "template_releases", "worker_commands", "stat_workers", "unknown_cluster_aliases", "unknown_mappings", "unknown_templates", "templates", "template_state", "content_releases", "schema_migrations")
		}},
	); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) CleanupExpired(ctx context.Context) error {
	now := time.Now().UTC()
	db := s.DB.WithContext(ctx)
	if err := db.Where(clause.Lt{Column: "expires_at", Value: now}).Delete(&Cl1Dedup{}).Error; err != nil {
		return err
	}
	return db.Where(clause.Lt{Column: "created_at", Value: now.Add(-30 * 24 * time.Hour)}).Delete(&PublishHistory{}).Error
}
