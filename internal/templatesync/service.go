// Package templatesync receives confirmed, durable events from the independent template service.
package templatesync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"ap-cloud-server/internal/v3db"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrConflict = errors.New("event ID reused with a different body")
var eventIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var itemIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_. -]{0,127}$`)

type Event struct {
	EventID   string          `json:"event_id"`
	Sequence  uint64          `json:"sequence"`
	Type      string          `json:"type"`
	Revision  uint64          `json:"revision"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt string          `json:"created_at"`
}

type Publication struct {
	Revision   uint64                     `json:"revision"`
	ReplaceIDs []v3db.TemplateItemMapping `json:"replace_ids"`
}

func (event Event) Validate() error {
	if !eventIDPattern.MatchString(event.EventID) || event.Sequence == 0 {
		return errors.New("invalid event identity")
	}
	if _, err := time.Parse(time.RFC3339Nano, event.CreatedAt); err != nil {
		return errors.New("invalid event timestamp")
	}
	if event.Type != "template.registered" && event.Type != "release.published" && event.Type != "release.failed" {
		return errors.New("unsupported template event")
	}
	_, err := event.publication()
	return err
}

func (event Event) publication() (*Publication, error) {
	if event.Type != "release.published" {
		return nil, nil
	}
	var result Publication
	if err := json.Unmarshal(event.Payload, &result); err != nil || result.Revision == 0 || result.Revision != event.Revision {
		return nil, errors.New("invalid release payload")
	}
	seen := make(map[string]bool)
	for _, mapping := range result.ReplaceIDs {
		if !strings.HasPrefix(mapping.UnknownID, "U-") || !itemIDPattern.MatchString(mapping.UnknownID) ||
			!itemIDPattern.MatchString(mapping.ItemID) || strings.HasPrefix(mapping.ItemID, "U-") ||
			strings.TrimSpace(mapping.Name) == "" || len(mapping.Name) > 512 || len(mapping.Tag) > 128 || seen[mapping.UnknownID] {
			return nil, errors.New("invalid item mapping")
		}
		seen[mapping.UnknownID] = true
	}
	return &result, nil
}

type Service struct {
	db   *gorm.DB
	wake chan<- struct{}
}

func New(db *gorm.DB, wake chan<- struct{}) *Service { return &Service{db: db, wake: wake} }

func (s *Service) Revision(ctx context.Context) (uint64, error) {
	var state v3db.TemplateSyncState
	err := s.db.WithContext(ctx).Take(&state, uint8(1)).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	return state.Revision, err
}

func (s *Service) Apply(ctx context.Context, event Event, bodyHash string) error {
	publication, err := event.publication()
	if err != nil {
		return err
	}
	changed := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		seed := v3db.TemplateSyncState{ID: 1}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&seed).Error; err != nil {
			return err
		}
		var state v3db.TemplateSyncState
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&state, uint8(1)).Error; err != nil {
			return err
		}
		var receipt v3db.TemplateEventReceipt
		err := tx.Take(&receipt, clause.Eq{Column: "event_id", Value: event.EventID}).Error
		if err == nil {
			if receipt.BodyHash != bodyHash {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if publication != nil && publication.Revision > state.Revision {
			var changedIDs []any
			for _, mapping := range publication.ReplaceIDs {
				var old v3db.TemplateItemMapping
				err := tx.Take(&old, clause.Eq{Column: "unknown_id", Value: mapping.UnknownID}).Error
				if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
				if err != nil || old.ItemID != mapping.ItemID || old.Tag != mapping.Tag {
					changedIDs = append(changedIDs, mapping.UnknownID)
				}
				mapping.Revision = publication.Revision
				if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&mapping).Error; err != nil {
					return err
				}
			}
			if err := markAffectedHours(tx, changedIDs); err != nil {
				return err
			}
			state.Revision = publication.Revision
			changed = len(changedIDs) > 0
		}
		if event.Sequence > state.LastSequence {
			state.LastSequence = event.Sequence
		}
		state.UpdatedAt = time.Now().UTC()
		if err := tx.Save(&state).Error; err != nil {
			return err
		}
		return tx.Create(&v3db.TemplateEventReceipt{EventID: event.EventID, BodyHash: bodyHash,
			Sequence: event.Sequence, CreatedAt: time.Now().UTC()}).Error
	})
	if err == nil && changed && s.wake != nil {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
	return err
}

// Record values remain U-IDs. Only affected aggregate hours are recomputed with the new aliases.
func markAffectedHours(tx *gorm.DB, unknownIDs []any) error {
	if len(unknownIDs) == 0 {
		return nil
	}
	hours := make(map[time.Time]bool)
	for _, model := range []any{&v3db.CommissionItem{}, &v3db.ResearchItem{}} {
		var ids []string
		if err := tx.Model(model).Distinct("record_id").Where(clause.IN{Column: "item", Values: unknownIDs}).Pluck("record_id", &ids).Error; err != nil {
			return err
		}
		for start := 0; start < len(ids); start += 1000 {
			end := min(start+1000, len(ids))
			values := make([]any, 0, end-start)
			for _, id := range ids[start:end] {
				values = append(values, id)
			}
			var events []v3db.DropEvent
			if err := tx.Select("record_id", "collected_at").Where(clause.IN{Column: "record_id", Values: values}).Find(&events).Error; err != nil {
				return err
			}
			for _, event := range events {
				hours[event.CollectedAt.UTC().Truncate(time.Hour)] = true
			}
		}
	}
	for hour := range hours {
		if err := v3db.MarkDirtyHour(tx, hour); err != nil {
			return fmt.Errorf("mark mapped history: %w", err)
		}
	}
	return nil
}
