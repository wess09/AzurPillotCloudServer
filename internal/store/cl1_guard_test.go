package store

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm/clause"
)

// Persistence/dedup tests start with an already observed source; probation is
// exercised separately below rather than bypassed in production code.
func approveCl1ForTest(t *testing.T, s *Store, r Cl1Record) {
	t.Helper()
	now := time.Now().UTC()
	o := Cl1Observation{DeviceID: r.DeviceID, InstanceID: r.InstanceID, Month: r.Month, Checks: cl1RequiredChecks,
		StartedAt: now.Add(-time.Hour), StartBattles: r.BattleCount, StartEncounters: r.AkashiEncounters, StartAP: r.AkashiAP,
		LastReceivedAt: now.Add(-time.Minute), LastCollectedAt: r.CollectedAt.Add(-time.Minute), LastCheckAt: now.Add(-time.Minute),
		LastBattles: r.BattleCount, LastEncounters: r.AkashiEncounters, LastAP: r.AkashiAP}
	if err := s.DB.Create(&o).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, model := range []any{&Cl1Source{}, &Cl1Observation{}, &Cl1Snapshot{}, &Cl1Dedup{}} {
			s.DB.Where(clause.Eq{Column: "device_id", Value: r.DeviceID}).Delete(model)
		}
	})
}

func TestCl1ProbationBanAndExpiry(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	r := Cl1Record{DeviceID: uniqueID("guard"), InstanceID: "instance", Month: "2026-11", BattleCount: 100, AkashiEncounters: 5, AkashiAP: 500}
	defer func() {
		for _, model := range []any{&Cl1Source{}, &Cl1Observation{}, &Cl1Snapshot{}, &Cl1Dedup{}} {
			s.DB.Where(clause.Eq{Column: "device_id", Value: r.DeviceID}).Delete(model)
		}
	}()
	submit := func(at time.Time, battles int64) Cl1Outcome {
		t.Helper()
		r.RequestID, r.CollectedAt, r.ReceivedAt, r.BattleCount = uniqueID("guard-request"), at, at, battles
		out, _, err := s.SubmitCl1(ctx, r, r.RequestID, []byte(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if out := submit(now, 100); out != Cl1Observed {
		t.Fatal("first observation published", out)
	}
	if out, _, err := s.SubmitCl1(ctx, r, r.RequestID, []byte(`{}`)); err != nil || out != Cl1Duplicate {
		t.Fatal("retry advanced observation", out, err)
	}
	if out := submit(now.Add(time.Second), 100); out != Cl1Observed {
		t.Fatal(out)
	}
	if out := submit(now.Add(30*time.Second), 102); out != Cl1Observed {
		t.Fatal(out)
	}
	if rows, err := s.ListCl1Snapshots(ctx, r.DeviceID, r.Month, 10); err != nil || len(rows) != 0 {
		t.Fatal("unverified observations leaked", rows, err)
	}
	if out := submit(now.Add(time.Minute), 104); out != Cl1Accepted {
		t.Fatal("third spaced observation not published", out)
	}
	if out := submit(now.Add(61*time.Second), 8000); out != Cl1Discarded {
		t.Fatal("counter jump not blocked", out)
	}
	var source Cl1Source
	if err := s.DB.Where(clause.Eq{Column: "device_id", Value: r.DeviceID}).Take(&source).Error; err != nil || source.BlockedUntil == nil {
		t.Fatal(source, err)
	}
	until := *source.BlockedUntil
	if !until.Equal(now.Add(61*time.Second + 24*time.Hour)) {
		t.Fatal("ban is not 24 hours", until)
	}
	if out := submit(now.Add(time.Hour), 106); out != Cl1Discarded {
		t.Fatal("blocked source published", out)
	}
	if out := submit(until.Add(time.Second), 106); out != Cl1Observed {
		t.Fatal("expiry skipped re-observation", out)
	}
	rows, err := s.ListCl1Snapshots(ctx, r.DeviceID, r.Month, 10)
	if err != nil || len(rows) != 1 || rows[0].BattleCount != 104 {
		t.Fatal("discard changed approved statistics", rows, err)
	}
}

func TestCl1AbsoluteAndGrowthLimits(t *testing.T) {
	now := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	r := Cl1Record{Month: "2026-11", CollectedAt: now, BattleCount: 1200, AkashiEncounters: 60, AkashiAP: 4200}
	if !Cl1TotalsValid(r, now) {
		t.Fatal("ordinary counters rejected")
	}
	for _, change := range []func(*Cl1Record){
		func(r *Cl1Record) { r.BattleCount = 1000000000000 },
		func(r *Cl1Record) { r.AkashiAP = 1000000000000000 },
		func(r *Cl1Record) { r.AkashiEncounters = 601 },
		func(r *Cl1Record) { r.AkashiEncounters = 0 },
		func(r *Cl1Record) { r.CollectedAt = now.Add(time.Hour) },
		func(r *Cl1Record) { r.Month = "2026-12" },
	} {
		bad := r
		change(&bad)
		if Cl1TotalsValid(bad, now) {
			t.Fatal("invalid counters accepted", bad)
		}
	}
	o := Cl1Observation{StartedAt: now.Add(-5 * time.Minute), LastReceivedAt: now.Add(-time.Minute),
		StartBattles: 1180, LastBattles: 1198, StartEncounters: 59, LastEncounters: 60, StartAP: 4000, LastAP: 4200}
	if !cl1GrowthValid(o, r, now) {
		t.Fatal("ordinary growth rejected")
	}
	r.BattleCount = 1100
	if cl1GrowthValid(o, r, now) {
		t.Fatal("counter regression accepted")
	}
}
