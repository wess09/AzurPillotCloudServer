package templatesync

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"ap-cloud-server/internal/drops"
	"ap-cloud-server/internal/v3db"
	"gorm.io/gorm/clause"
)

func TestPublishedMappingRepairsHistoryAndRejectsConflictingReplay(t *testing.T) {
	dsn := os.Getenv("TEMPLATE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEMPLATE_TEST_DATABASE_URL to a dedicated PostgreSQL schema")
	}
	ctx := context.Background()
	store, err := v3db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	for _, eventID := range []string{"e459b41d-2db6-454f-9187-ff6d226d79f1", "e459b41d-2db6-454f-9187-ff6d226d79f2"} {
		if err := store.DB.Where(clause.Eq{Column: "event_id", Value: eventID}).Delete(&v3db.TemplateEventReceipt{}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := store.DB.Where(clause.Eq{Column: "id", Value: 1}).Delete(&v3db.TemplateSyncState{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.DB.Where(clause.Eq{Column: "unknown_id", Value: "U-SYNC-TEST"}).Delete(&v3db.TemplateItemMapping{}).Error; err != nil {
		t.Fatal(err)
	}
	id := "template-sync-test-record"
	if err := store.DB.Where(clause.Eq{Column: "record_id", Value: id}).Delete(&v3db.CommissionItem{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.DB.Where(clause.Eq{Column: "record_id", Value: id}).Delete(&v3db.DropEvent{}).Error; err != nil {
		t.Fatal(err)
	}
	hour := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	event := v3db.DropEvent{RecordID: id, Scene: v3db.SceneCommission, Server: "cn", Comm: "Test Escort", CollectedAt: hour, ReceivedAt: hour}
	if err := store.DB.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.DB.Create(&v3db.CommissionItem{RecordID: id, Item: "U-SYNC-TEST", Amount: 12}).Error; err != nil {
		t.Fatal(err)
	}
	wake := make(chan struct{}, 1)
	service := New(store.DB, wake)
	publication, _ := json.Marshal(Publication{Revision: 100, ReplaceIDs: []v3db.TemplateItemMapping{{UnknownID: "U-SYNC-TEST", ItemID: "BlueprintT3", Name: "测试图纸", Tag: ""}}})
	notification := Event{EventID: "e459b41d-2db6-454f-9187-ff6d226d79f1", Sequence: 2, Type: "release.published", Revision: 100, Payload: publication, CreatedAt: hour.Format(time.RFC3339)}
	if err := notification.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := service.Apply(ctx, notification, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if err := service.Apply(ctx, notification, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if err := service.Apply(ctx, notification, strings.Repeat("b", 64)); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict: %v", err)
	}
	select {
	case <-wake:
	default:
		t.Fatal("mapping did not wake aggregator")
	}
	if err := drops.NewAggregator(store.DB, time.Hour, nil).RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var raw v3db.CommissionItem
	if err := store.DB.Take(&raw, clause.Eq{Column: "record_id", Value: id}).Error; err != nil {
		t.Fatal(err)
	}
	if raw.Item != "U-SYNC-TEST" || raw.Amount != 12 {
		t.Fatalf("raw history changed: %+v", raw)
	}
	var aggregate v3db.CommissionItemHourly
	if err := store.DB.Take(&aggregate, clause.Eq{Column: "item", Value: "BlueprintT3"}).Error; err != nil {
		t.Fatal(err)
	}
	if aggregate.Amount != 12 || aggregate.Occurrences != 1 {
		t.Fatalf("history did not resolve: %+v", aggregate)
	}
	// A delayed earlier release must not revert the published mapping.
	older := notification
	older.EventID = "e459b41d-2db6-454f-9187-ff6d226d79f2"
	older.Sequence, older.Revision = 1, 99
	older.Payload = json.RawMessage(`{"revision":99,"replace_ids":[{"unknown_id":"U-SYNC-TEST","item_id":"WrongItem","name":"旧映射","tag":""}]}`)
	if err := service.Apply(ctx, older, strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	var mapping v3db.TemplateItemMapping
	if err := store.DB.Take(&mapping, clause.Eq{Column: "unknown_id", Value: "U-SYNC-TEST"}).Error; err != nil {
		t.Fatal(err)
	}
	if mapping.ItemID != "BlueprintT3" {
		t.Fatal("older release replaced mapping")
	}
}

func TestValidateRejectsUnknownEventAndTemporaryTarget(t *testing.T) {
	event := Event{EventID: "e459b41d-2db6-454f-9187-ff6d226d79f1", Sequence: 1, Type: "release.published", Revision: 1, CreatedAt: "2026-10-30T00:00:00Z", Payload: json.RawMessage(`{"revision":1,"replace_ids":[{"unknown_id":"U-TEST","item_id":"U-OTHER","name":"错误"}]}`)}
	if event.Validate() == nil {
		t.Fatal("temporary mapping target accepted")
	}
	event.Type = "arbitrary"
	if event.Validate() == nil {
		t.Fatal("unsupported event accepted")
	}
}
