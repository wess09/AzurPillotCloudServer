package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type MaintenanceSource struct {
	Server    string       `json:"server"`
	Title     string       `json:"title"`
	URL       string       `json:"url"`
	Plan      *Maintenance `json:"plan"`
	CheckedAt *time.Time   `json:"checked_at"`
	SyncedAt  *time.Time   `json:"synced_at"`
	Error     string       `json:"error"`
}

func (s *Store) MaintenanceSource(ctx context.Context) (MaintenanceSource, error) {
	out := MaintenanceSource{Server: "cn"}
	var row OfficialMaintenance
	err := s.DB.WithContext(ctx).Take(&row, clause.Eq{Column: "server", Value: "cn"}).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.Title, out.URL, out.CheckedAt, out.SyncedAt, out.Error = row.Title, row.SourceURL, &row.CheckedAt, row.SyncedAt, row.Error
	if row.StartsAt != nil && row.SyncedAt != nil {
		out.Plan = &Maintenance{Server: row.Server, Name: row.Name, StartsAt: *row.StartsAt, EndsAt: row.EndsAt, UpdatedAt: *row.SyncedAt, Source: "official", SourceURL: row.SourceURL}
	}
	return out, nil
}

func (s *Store) SaveOfficialMaintenance(ctx context.Context, m Maintenance, title, sourceURL string) error {
	if m.Server != "cn" || m.EndsAt == nil || !m.EndsAt.After(m.StartsAt) {
		return fmt.Errorf("invalid official maintenance")
	}
	now := time.Now().UTC()
	row := OfficialMaintenance{Server: "cn", Name: m.Name, StartsAt: &m.StartsAt, EndsAt: m.EndsAt, Title: title, SourceURL: sourceURL, CheckedAt: now, SyncedAt: &now}
	return s.DB.WithContext(ctx).Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error
}

// Sync failures update status without erasing the previous successful plan.
func (s *Store) RecordMaintenanceSyncError(ctx context.Context, message string) error {
	row := OfficialMaintenance{Server: "cn", CheckedAt: time.Now().UTC(), Error: message}
	return s.DB.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "server"}}, DoUpdates: clause.AssignmentColumns([]string{"checked_at", "error"})}).Create(&row).Error
}
