package v3db

import (
	"ap-cloud-server/internal/database"
	"context"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

type Store struct{ *database.Connection }

// Open is used by isolated integration tests. Production shares the core pool.
func Open(ctx context.Context, dsn string) (*Store, error) {
	connection, err := database.Open(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err = Migrate(ctx, connection.DB); err != nil {
		_ = connection.Close()
		return nil, err
	}
	return &Store{Connection: connection}, nil
}

func Migrate(ctx context.Context, db *gorm.DB) error {
	return database.Migrate(ctx, db, "statistics", database.Migration{Version: 1, Apply: func(tx *gorm.DB) error {
		if err := tx.AutoMigrate(AllModels()...); err != nil {
			return err
		}
		return tx.Migrator().DropTable("schema_guard")
	}})
}

func MarkDirtyHour(tx *gorm.DB, hour time.Time) error {
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&DirtyHour{Hour: hour.UTC().Truncate(time.Hour), CreatedAt: time.Now().UTC()}).Error
}
