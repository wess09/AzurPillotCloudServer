package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"ap-cloud-server/internal/database"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func TestSchemaContainsOnlyActiveDomains(t *testing.T) {
	s := openTestStore(t)
	for _, model := range coreModels() {
		if !s.DB.Migrator().HasTable(model) {
			t.Fatalf("missing ORM model %T", model)
		}
	}
	for _, name := range []string{"stat_results", "stat_events", "stat_workers", "template_candidates", "templates", "unknown_templates", "content_releases", "schema_migrations"} {
		if s.DB.Migrator().HasTable(name) {
			t.Errorf("retired table %s remains", name)
		}
	}
}

func TestVersionedMigrationsAreOrderedAtomicAndRestartable(t *testing.T) {
	s := openTestStore(t)
	component := uniqueID("migration")
	ctx := context.Background()
	t.Cleanup(func() { s.DB.Where(clause.Eq{Column: "component", Value: component}).Delete(&database.SchemaVersion{}) })
	calls := 0
	failure := errors.New("migration failed")
	steps := []database.Migration{
		{Version: 1, Apply: func(tx *gorm.DB) error { calls++; return nil }},
		{Version: 2, Apply: func(tx *gorm.DB) error { calls++; return failure }},
	}
	if err := database.Migrate(ctx, s.DB, component, steps...); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	var version database.SchemaVersion
	if err := s.DB.Take(&version, clause.Eq{Column: "component", Value: component}).Error; err != nil || version.Version != 0 {
		t.Fatalf("partial migration committed: %+v %v", version, err)
	}
	steps[1].Apply = func(tx *gorm.DB) error { calls++; return nil }
	if err := database.Migrate(ctx, s.DB, component, steps...); err != nil {
		t.Fatal(err)
	}
	if calls != 4 {
		t.Fatalf("rollback did not rerun all pending steps: %d", calls)
	}
	if err := database.Migrate(ctx, s.DB, component, steps...); err != nil || calls != 4 {
		t.Fatalf("restart replayed migrations: %d %v", calls, err)
	}
	if err := database.Migrate(ctx, s.DB, component, steps[:1]...); err == nil {
		t.Fatal("older binary accepted newer schema")
	}
}

func TestPublishHistoryKeepsLatestWorkerResult(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	worker := uniqueID("worker")
	t.Cleanup(func() { s.DB.Where(clause.Eq{Column: "worker", Value: worker}).Delete(&PublishHistory{}) })
	for _, status := range []string{"failed", "success"} {
		if err := s.RecordPublish(ctx, PublishRecord{Worker: worker, Status: status, CreatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	results, err := s.LatestPublishByWorker(ctx)
	if err != nil || results[worker].Status != "success" {
		t.Fatalf("latest publication: %+v %v", results, err)
	}
}
