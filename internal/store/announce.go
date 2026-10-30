package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"ap-cloud-server/internal/database"
	"gorm.io/gen/field"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Announcement struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Content   *string   `json:"content"`
	URL       *string   `json:"url"`
	Active    bool      `json:"active"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s *Store) GenerateAnnouncementID(ctx context.Context, now time.Time) (string, error) {
	date := now.UTC().Format("20060102")
	var count int64
	err := s.DB.WithContext(ctx).Model(&AnnouncementRecord{}).Where(clause.Like{Column: "id", Value: "notice-" + date + "-%"}).Count(&count).Error
	return fmt.Sprintf("notice-%s-%03d", date, count+1), err
}

func (s *Store) SetAnnouncement(ctx context.Context, a Announcement) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Serialize active-announcement changes across instances.
		var guard database.SchemaVersion
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&guard, clause.Eq{Column: "component", Value: "core"}).Error; err != nil {
			return err
		}
		if err := tx.Model(&AnnouncementRecord{}).Where(clause.Eq{Column: "active", Value: true}).Update("active", false).Error; err != nil {
			return err
		}
		row := AnnouncementRecord{ID: a.ID, Title: a.Title, Content: a.Content, URL: a.URL, Active: true}
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, DoUpdates: clause.AssignmentColumns([]string{"title", "content", "url", "active", "updated_at"})}).Create(&row).Error
	})
}

func (s *Store) WithdrawAnnouncement(ctx context.Context) error {
	return s.DB.WithContext(ctx).Model(&AnnouncementRecord{}).Where(clause.Eq{Column: "active", Value: true}).Update("active", false).Error
}

func announcement(row AnnouncementRecord) Announcement {
	return Announcement{ID: row.ID, Title: row.Title, Content: row.Content, URL: row.URL, Active: row.Active, UpdatedAt: row.UpdatedAt}
}

func (s *Store) LatestAnnouncement(ctx context.Context) (*Announcement, error) {
	var row AnnouncementRecord
	err := s.DB.WithContext(ctx).Where(clause.Eq{Column: "active", Value: true}).Order(database.Desc("updated_at")).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := announcement(row)
	return &out, nil
}

func (s *Store) ListAnnouncements(ctx context.Context, limit int) ([]Announcement, error) {
	var rows []AnnouncementRecord
	if err := s.DB.WithContext(ctx).Order(database.Desc("updated_at")).Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]Announcement, 0, len(rows))
	for _, row := range rows {
		out = append(out, announcement(row))
	}
	return out, nil
}

func (s *Store) GetUpdatePolicy(ctx context.Context) (bool, error) {
	var row UpdatePolicy
	err := s.DB.WithContext(ctx).Take(&row, clause.Eq{Column: "id", Value: true}).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return row.ForceUpdate, err
}

func (s *Store) SetUpdatePolicy(ctx context.Context, force bool) error {
	return s.DB.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, DoUpdates: clause.AssignmentColumns([]string{"force_update", "updated_at"})}).Create(&UpdatePolicy{ID: true, ForceUpdate: force}).Error
}

type PublishRecord struct {
	Worker      string    `json:"worker"`
	ContentHash string    `json:"content_hash"`
	Status      string    `json:"status"`
	Error       string    `json:"error,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

func (s *Store) RecordPublish(ctx context.Context, rec PublishRecord) error {
	row := PublishHistory{Worker: rec.Worker, ContentHash: rec.ContentHash, Status: rec.Status, CreatedAt: rec.CreatedAt}
	if rec.Error != "" {
		row.Error = &rec.Error
	}
	return s.DB.WithContext(ctx).Create(&row).Error
}

func (s *Store) LatestPublishByWorker(ctx context.Context) (map[string]PublishRecord, error) {
	db := s.DB.WithContext(ctx)
	latest := database.Group(database.Project(db.Model(&PublishHistory{}), field.NewInt64("", "id").Max()), "worker")
	var rows []PublishHistory
	if err := db.Where(field.ContainsSubQuery([]field.Expr{field.NewInt64("", "id")}, latest)).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]PublishRecord, len(rows))
	for _, row := range rows {
		rec := PublishRecord{Worker: row.Worker, ContentHash: row.ContentHash, Status: row.Status, CreatedAt: row.CreatedAt}
		if row.Error != nil {
			rec.Error = *row.Error
		}
		out[row.Worker] = rec
	}
	return out, nil
}
