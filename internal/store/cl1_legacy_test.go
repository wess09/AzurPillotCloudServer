package store

import (
	"context"
	"gorm.io/gorm/clause"
	"testing"
	"time"
)

func TestLegacyCumulativeGuardAllowsV2Corrections(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	device := uniqueID("legacy-device")
	r := Cl1Record{RequestID: uniqueID("r1"), DeviceID: device, InstanceID: "instance", Month: "2026-10", CollectedAt: time.Now().UTC(), BattleCount: 100, BattleRounds: 50, AkashiEncounters: 5, AkashiAP: 500, Legacy: true}
	t.Cleanup(func() {
		s.DB.WithContext(ctx).Where(clause.Eq{Column: "device_id", Value: device}).Delete(&Cl1Snapshot{})
		s.DB.WithContext(ctx).Where(clause.Eq{Column: "device_id", Value: device}).Delete(&Cl1Dedup{})
	})
	if out, _, err := s.SubmitCl1(ctx, r, "hash1", []byte(`{}`)); err != nil || out != Cl1Accepted {
		t.Fatal(out, err)
	}
	r.RequestID = uniqueID("r2")
	r.CollectedAt = r.CollectedAt.Add(time.Second)
	r.BattleCount = 90
	if out, _, err := s.SubmitCl1(ctx, r, "hash2", []byte(`{}`)); err != nil || out != Cl1StaleSnapshot {
		t.Fatal("legacy regression accepted", out, err)
	}
	r.RequestID = uniqueID("r3")
	r.Legacy = false
	if out, _, err := s.SubmitCl1(ctx, r, "hash3", []byte(`{}`)); err != nil || out != Cl1Accepted {
		t.Fatal("v2 correction rejected", out, err)
	}
}
