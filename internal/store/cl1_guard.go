package store

import (
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	cl1RequiredChecks = 3
	cl1CheckSpacing   = 30 * time.Second
	cl1BlockDuration  = 24 * time.Hour
	cl1MaxAPPerVisit  = int64(10000)
)

var cl1Location = time.FixedZone("Asia/Shanghai", 8*60*60)

// Cl1TotalsValid uses deliberately generous physical bounds, not drop-rate estimates.
// Growth is checked separately against trusted server reception times.
func Cl1TotalsValid(rec Cl1Record, now time.Time) bool {
	month, err := time.ParseInLocation("2006-01", rec.Month, cl1Location)
	if err != nil || rec.BattleCount < 1 || rec.AkashiEncounters < 0 || rec.AkashiAP < 0 {
		return false
	}
	if month.After(now.In(cl1Location)) || rec.CollectedAt.Before(month.Add(-5*time.Minute)) || rec.CollectedAt.After(now.Add(5*time.Minute)) {
		return false
	}
	end := month.AddDate(0, 1, 0)
	if rec.CollectedAt.Before(end) {
		end = rec.CollectedAt
	}
	// Even one battle per second leaves ample room for normal play and clock skew.
	maxBattles := int64(end.Sub(month).Seconds()) + 300
	rounds := rec.BattleCount/2 + rec.BattleCount%2
	return rec.BattleCount <= maxBattles && rec.AkashiEncounters <= rounds && rec.AkashiAP <= rec.AkashiEncounters*cl1MaxAPPerVisit
}

func cl1GrowthValid(o Cl1Observation, r Cl1Record, now time.Time) bool {
	if r.BattleCount < o.LastBattles || r.AkashiEncounters < o.LastEncounters || r.AkashiAP < o.LastAP {
		return false
	}
	// An anchor stops repeated small, fast jumps from bypassing a per-request limit.
	for _, base := range []struct {
		at                      time.Time
		battles, encounters, ap int64
	}{{o.LastReceivedAt, o.LastBattles, o.LastEncounters, o.LastAP}, {o.StartedAt, o.StartBattles, o.StartEncounters, o.StartAP}} {
		elapsed := now.Sub(base.at)
		if elapsed < 0 {
			return false
		}
		battles := r.BattleCount - base.battles
		encounters := r.AkashiEncounters - base.encounters
		ap := r.AkashiAP - base.ap
		if battles > int64(elapsed.Seconds())+120 || encounters > (battles+1)/2+2 || ap > (encounters+1)*cl1MaxAPPerVisit {
			return false
		}
	}
	return true
}

func cl1SourceCondition(r Cl1Record) []clause.Expression {
	return []clause.Expression{clause.Eq{Column: "device_id", Value: r.DeviceID}, clause.Eq{Column: "instance_id", Value: r.InstanceID}}
}

// lockCl1Source serializes all months for a pair, so changing the month cannot bypass a ban.
func lockCl1Source(tx *gorm.DB, r Cl1Record, now time.Time) (Cl1Source, error) {
	source := Cl1Source{DeviceID: r.DeviceID, InstanceID: r.InstanceID}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&source).Error; err != nil {
		return source, err
	}
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(clause.And(cl1SourceCondition(r)...)).Take(&source).Error; err != nil {
		return source, err
	}
	if source.BlockedUntil != nil && !now.Before(*source.BlockedUntil) {
		if err := tx.Where(clause.And(cl1SourceCondition(r)...)).Delete(&Cl1Observation{}).Error; err != nil {
			return source, err
		}
		source.BlockedUntil, source.BlockReason = nil, ""
		if err := tx.Save(&source).Error; err != nil {
			return source, err
		}
	}
	return source, nil
}

func observeCl1(tx *gorm.DB, source *Cl1Source, r Cl1Record, now time.Time) (Cl1Outcome, error) {
	block := func(reason string) (Cl1Outcome, error) {
		until := now.Add(cl1BlockDuration)
		source.BlockedUntil, source.BlockReason = &until, reason
		return Cl1Discarded, tx.Save(source).Error
	}
	if !Cl1TotalsValid(r, now) {
		return block("invalid cumulative counters or timestamp")
	}
	conditions := append(cl1SourceCondition(r), clause.Eq{Column: "month", Value: r.Month})
	o := Cl1Observation{DeviceID: r.DeviceID, InstanceID: r.InstanceID, Month: r.Month}
	err := tx.Where(clause.And(conditions...)).Take(&o).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return Cl1Discarded, err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// Existing approved totals supply a lower bound, but do not skip probation.
		var previous Cl1Snapshot
		err = tx.Where(clause.And(conditions...)).Take(&previous).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return Cl1Discarded, err
		}
		if err == nil {
			o.StartedAt, o.LastReceivedAt = previous.UpdatedAt, previous.UpdatedAt
			o.LastCollectedAt = previous.CollectedAt
			o.StartBattles, o.LastBattles = previous.BattleCount, previous.BattleCount
			o.StartEncounters, o.LastEncounters = previous.AkashiEncounters, previous.AkashiEncounters
			o.StartAP, o.LastAP = previous.AkashiAP, previous.AkashiAP
		}
	}
	if !o.LastCollectedAt.IsZero() && !r.CollectedAt.After(o.LastCollectedAt) {
		return Cl1Observed, nil // Delayed or repeated observations do not advance probation.
	}
	if !o.LastReceivedAt.IsZero() && now.Before(o.LastReceivedAt) {
		return Cl1Observed, nil // Concurrent requests can acquire the pair lock out of arrival order.
	}
	if !o.StartedAt.IsZero() && !cl1GrowthValid(o, r, now) {
		return block("abnormal cumulative counter growth")
	}
	if o.StartedAt.IsZero() {
		o.StartedAt = now
		o.StartBattles, o.StartEncounters, o.StartAP = r.BattleCount, r.AkashiEncounters, r.AkashiAP
	}
	if o.Checks < cl1RequiredChecks && (o.LastCheckAt.IsZero() || now.Sub(o.LastCheckAt) >= cl1CheckSpacing) {
		o.Checks++
		o.LastCheckAt = now
	}
	o.LastReceivedAt, o.LastCollectedAt = now, r.CollectedAt
	o.LastBattles, o.LastEncounters, o.LastAP = r.BattleCount, r.AkashiEncounters, r.AkashiAP
	if err := tx.Save(&o).Error; err != nil {
		return Cl1Discarded, err
	}
	if o.Checks < cl1RequiredChecks {
		return Cl1Observed, nil
	}
	return Cl1Accepted, nil
}
