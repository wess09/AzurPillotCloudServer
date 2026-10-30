package store

import (
	"context"
	"gorm.io/gorm/clause"
	"testing"
	"time"
)

func TestOfficialMaintenancePrecedenceAndFailurePersistence(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	cleanup := func() {
		err := s.DB.WithContext(ctx).Where(clause.Eq{Column: "server", Value: "cn"}).Delete(&MaintenanceSchedule{}).Error
		if err == nil {
			err = s.DB.WithContext(ctx).Where(clause.Eq{Column: "server", Value: "cn"}).Delete(&OfficialMaintenance{}).Error
		}
		if err != nil {
			t.Error(err)
		}
	}
	cleanup()
	defer cleanup()
	start := time.Now().Add(time.Hour)
	end := start.Add(6 * time.Hour)
	plan := Maintenance{Server: "cn", Name: "official", StartsAt: start, EndsAt: &end}
	if err := s.SaveOfficialMaintenance(ctx, plan, "official title", "https://game.bilibili.com/blhx/news/#news_detail_id=2"); err != nil {
		t.Fatal(err)
	}
	got, err := s.LatestMaintenance(ctx, "cn")
	if err != nil || got == nil || got.Source != "official" {
		t.Fatal(got, err)
	}
	if err = s.RecordMaintenanceSyncError(ctx, "network unavailable"); err != nil {
		t.Fatal(err)
	}
	state, err := s.MaintenanceSource(ctx)
	if err != nil || state.Plan == nil || state.Title != "official title" || state.Error == "" || state.SyncedAt == nil {
		t.Fatal(state, err)
	}
	manual := Maintenance{Server: "cn", Name: "manual", StartsAt: start}
	if err = s.UpsertMaintenance(ctx, manual); err != nil {
		t.Fatal(err)
	}
	got, err = s.LatestMaintenance(ctx, "cn")
	if err != nil || got.Source != "manual" {
		t.Fatal(got, err)
	}
	if _, err = s.DeleteMaintenance(ctx, "cn"); err != nil {
		t.Fatal(err)
	}
	got, err = s.LatestMaintenance(ctx, "cn")
	if err != nil || got.Source != "official" {
		t.Fatal(got, err)
	}
	end = time.Now().Add(-time.Hour)
	plan.StartsAt = end.Add(-6 * time.Hour)
	plan.EndsAt = &end
	if err = s.SaveOfficialMaintenance(ctx, plan, "past maintenance", "https://example.invalid/official"); err != nil {
		t.Fatal(err)
	}
	got, err = s.LatestMaintenance(ctx, "cn")
	if err != nil || got != nil {
		t.Fatal(got, err)
	}
	if err = s.RecordMaintenanceSyncError(ctx, "source unavailable"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.LatestMaintenance(ctx, "cn"); err == nil {
		t.Fatal("a failed source cannot claim there is no maintenance")
	}
}
