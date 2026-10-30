package store

import (
	"context"
	"fmt"
	"sort"
	"time"

	"gorm.io/gorm/clause"
)

type Maintenance struct {
	Server    string     `json:"server"`
	Name      string     `json:"name"`
	StartsAt  time.Time  `json:"starts_at"`
	EndsAt    *time.Time `json:"ends_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	Source    string     `json:"source,omitempty"`
	SourceURL string     `json:"source_url,omitempty"`
}

func (s *Store) UpsertMaintenance(ctx context.Context, m Maintenance) error {
	if !validServer(m.Server) || m.EndsAt != nil && !m.EndsAt.After(m.StartsAt) {
		return fmt.Errorf("invalid maintenance schedule")
	}
	row := MaintenanceSchedule{Server: m.Server, Name: m.Name, StartsAt: m.StartsAt, EndsAt: m.EndsAt}
	return s.DB.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "server"}}, DoUpdates: clause.AssignmentColumns([]string{"name", "starts_at", "ends_at", "updated_at"})}).Create(&row).Error
}

func validServer(server string) bool {
	return server == "cn" || server == "en" || server == "jp" || server == "tw"
}
func activePlan(m Maintenance, now time.Time) bool { return m.EndsAt == nil || !m.EndsAt.Before(now) }

// There are at most four manual rows and one official row. Merge precedence in
// domain code so it stays shared between admin and public readers.
func (s *Store) plans(ctx context.Context) ([]Maintenance, error) {
	var manual []MaintenanceSchedule
	if err := s.DB.WithContext(ctx).Find(&manual).Error; err != nil {
		return nil, err
	}
	out := make([]Maintenance, 0, len(manual)+1)
	for _, row := range manual {
		out = append(out, Maintenance{Server: row.Server, Name: row.Name, StartsAt: row.StartsAt, EndsAt: row.EndsAt, UpdatedAt: row.UpdatedAt, Source: "manual"})
	}
	source, err := s.MaintenanceSource(ctx)
	if err != nil {
		return nil, err
	}
	if source.Plan != nil {
		out = append(out, *source.Plan)
	}
	now := time.Now().UTC()
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Server != b.Server {
			return a.Server < b.Server
		}
		if activePlan(a, now) != activePlan(b, now) {
			return activePlan(a, now)
		}
		if a.Source != b.Source {
			return a.Source == "manual"
		}
		return a.StartsAt.After(b.StartsAt)
	})
	return out, nil
}

func (s *Store) LatestMaintenance(ctx context.Context, server string) (*Maintenance, error) {
	plans, err := s.plans(ctx)
	if err != nil {
		return nil, err
	}
	for _, plan := range plans {
		if plan.Server == server && activePlan(plan, time.Now().UTC()) {
			return &plan, nil
		}
	}
	if server == "cn" {
		source, err := s.MaintenanceSource(ctx)
		if err != nil {
			return nil, err
		}
		if source.Error != "" {
			return nil, fmt.Errorf("官方维护来源暂不可用: %s", source.Error)
		}
	}
	return nil, nil
}

func (s *Store) ListMaintenances(ctx context.Context) ([]Maintenance, error) {
	plans, err := s.plans(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Maintenance, 0, 4)
	seen := make(map[string]bool)
	for _, plan := range plans {
		if !seen[plan.Server] {
			seen[plan.Server] = true
			out = append(out, plan)
		}
	}
	return out, nil
}

func (s *Store) DeleteMaintenance(ctx context.Context, server string) (bool, error) {
	result := s.DB.WithContext(ctx).Where(clause.Eq{Column: "server", Value: server}).Delete(&MaintenanceSchedule{})
	return result.RowsAffected > 0, result.Error
}
