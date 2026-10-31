package store

import (
	"context"
	"sort"
	"time"
)

// Cl1Metrics contains public totals, never device or instance identifiers.
type Cl1Metrics struct {
	Devices           int     `json:"devices"`
	BattleCount       int64   `json:"battle_count"`
	BattleRounds      int64   `json:"battle_rounds"`
	AkashiEncounters  int64   `json:"akashi_encounters"`
	AkashiAP          int64   `json:"akashi_ap"`
	APCost            int64   `json:"ap_cost"`
	NetAP             int64   `json:"net_ap"`
	AkashiProbability float64 `json:"akashi_probability"`
	AverageAP         float64 `json:"average_ap"`
	CycleEfficiency   float64 `json:"cycle_efficiency"`
}

type Cl1Month struct {
	Month string `json:"month"`
	Cl1Metrics
}

type Cl1Statistics struct {
	Total     Cl1Metrics `json:"total"`
	History   []Cl1Month `json:"history"`
	UpdatedAt *time.Time `json:"updated_at"`
}

type cl1Accumulator struct {
	Cl1Metrics
	devices map[string]struct{}
}

func (a *cl1Accumulator) add(row Cl1Snapshot) {
	if a.devices == nil {
		a.devices = make(map[string]struct{})
	}
	a.devices[row.DeviceID] = struct{}{}
	a.BattleCount += row.BattleCount
	a.AkashiEncounters += row.AkashiEncounters
	a.AkashiAP += row.AkashiAP
}

func (a *cl1Accumulator) metrics() Cl1Metrics {
	m := a.Cl1Metrics
	m.Devices = len(a.devices)
	// Calculate once after summing all instances in this scope. Division and
	// remainder implement ceil(total battles / 2) without overflowing int64.
	m.BattleRounds = m.BattleCount/2 + m.BattleCount%2
	m.APCost = m.BattleRounds * 5
	m.NetAP = m.AkashiAP - m.APCost
	if m.BattleRounds > 0 {
		m.AkashiProbability = float64(m.AkashiEncounters) / float64(m.BattleRounds)
	}
	if m.AkashiEncounters > 0 {
		m.AverageAP = float64(m.AkashiAP) / float64(m.AkashiEncounters)
	}
	if m.APCost > 0 {
		m.CycleEfficiency = float64(m.NetAP) / float64(m.APCost)
	}
	return m
}

// Cl1Statistics sums each stored monthly cumulative snapshot once. A retried
// upload replaces that snapshot; it is never treated as another battle sample.
func (s *Store) Cl1Statistics(ctx context.Context) (Cl1Statistics, error) {
	out := Cl1Statistics{History: []Cl1Month{}}
	rows, err := s.DB.WithContext(ctx).Model(&Cl1Snapshot{}).
		Select("device_id", "month", "battle_count", "akashi_encounters", "akashi_ap", "updated_at").Rows()
	if err != nil {
		return out, err
	}
	defer rows.Close()
	var total cl1Accumulator
	months := make(map[string]*cl1Accumulator)
	for rows.Next() {
		var row Cl1Snapshot
		if err := s.DB.ScanRows(rows, &row); err != nil {
			return out, err
		}
		total.add(row)
		if months[row.Month] == nil {
			months[row.Month] = &cl1Accumulator{}
		}
		months[row.Month].add(row)
		if out.UpdatedAt == nil || row.UpdatedAt.After(*out.UpdatedAt) {
			updated := row.UpdatedAt
			out.UpdatedAt = &updated
		}
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	out.Total = total.metrics()
	for month, aggregate := range months {
		out.History = append(out.History, Cl1Month{Month: month, Cl1Metrics: aggregate.metrics()})
	}
	sort.Slice(out.History, func(i, j int) bool { return out.History[i].Month > out.History[j].Month })
	return out, nil
}
