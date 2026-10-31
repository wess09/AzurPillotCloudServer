package drops

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"ap-cloud-server/internal/database"
	"ap-cloud-server/internal/v3db"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Generated from pinned local game/client snapshots by the frontend catalog tool.
//
//go:embed analysis_catalog.json
var analysisCatalogJSON []byte

type analysisItem struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Category string   `json:"category"`
	Unit     string   `json:"unit"`
	Aliases  []string `json:"aliases"`
	Scenes   []string `json:"scenes"`
	Rarity   int      `json:"rarity"`
	Series   int      `json:"series"`
	Version  string   `json:"version"`
	GameID   string   `json:"game_id"`
	Faction  string   `json:"faction"`
	Subtype  string   `json:"subtype"`
}
type analysisProject struct {
	ID, Name, Category string
	Series, Duration   int
	Aliases            []string
	Condition          string
	Costs              []struct {
		Name   string `json:"name"`
		Amount int    `json:"amount"`
	} `json:"costs"`
}
type analysisCatalog struct {
	Items      []analysisItem
	Categories []struct{ ID, Name, Unit string }
	Research   map[string]analysisProject
	Commission map[string]analysisProject
}

var catalog = func() analysisCatalog {
	var value analysisCatalog
	if err := json.Unmarshal(analysisCatalogJSON, &value); err != nil {
		panic(err)
	}
	return value
}()

var commissionMetadata = func() map[string]analysisProject {
	index := make(map[string]analysisProject)
	for key, meta := range catalog.Commission {
		index[key] = meta
		for _, alias := range meta.Aliases {
			index[alias] = meta
		}
	}
	return index
}()

var ErrAnalysisQuery = errors.New("invalid analysis query")

type AnalysisQuery struct {
	From, To                       time.Time
	Scene, Server, Target, GroupBy string
	Category                       string
	Series, Duration               int
}

type AnalysisMetrics struct {
	Samples          int64    `json:"samples"`
	Occurrences      int64    `json:"occurrences"`
	Amount           int64    `json:"amount"`
	Min              int64    `json:"min"`
	Max              int64    `json:"max"`
	ExtraAmount      int64    `json:"extra_amount"`
	OtherAmount      int64    `json:"other_amount"`
	SuccessSamples   int64    `json:"success_samples"`
	SuccessAmount    int64    `json:"success_amount"`
	TotalSeconds     int64    `json:"total_seconds"`
	UnknownDurations int64    `json:"unknown_durations"`
	DropRate         float64  `json:"drop_rate"`
	Average          float64  `json:"average"`
	PerHour          *float64 `json:"per_hour"`
}

type AnalysisGroup struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Series    int    `json:"series"`
	Category  string `json:"category"`
	Duration  int    `json:"duration"`
	Condition string `json:"condition"`
	Costs     any    `json:"costs"`
	AnalysisMetrics
}

type AnalysisResult struct {
	Groups []AnalysisGroup `json:"groups"`
	AnalysisMetrics
	Unit string `json:"unit"`
}

func analysisTarget(q AnalysisQuery) (map[string]bool, string, error) {
	return analysisTargetItems(q, catalog.Items)
}

func analysisTargetItems(q AnalysisQuery, items []analysisItem) (map[string]bool, string, error) {
	if q.Scene != v3db.SceneResearch && q.Scene != v3db.SceneCommission {
		return nil, "", ErrAnalysisQuery
	}
	if q.GroupBy != "project" && q.GroupBy != "series" && q.GroupBy != "category" && q.GroupBy != "duration" {
		return nil, "", ErrAnalysisQuery
	}
	if q.Scene == v3db.SceneCommission && q.GroupBy == "series" {
		return nil, "", ErrAnalysisQuery
	}
	if q.Server != "" && !allowedServers[q.Server] {
		return nil, "", ErrAnalysisQuery
	}
	if !q.To.After(q.From) || q.To.Sub(q.From) > 366*24*time.Hour || q.Series < 0 || q.Series > 100 || q.Duration < 0 {
		return nil, "", ErrAnalysisQuery
	}
	selected, unit := map[string]bool{}, ""
	for _, item := range items {
		if q.Target != "item:"+item.ID && q.Target != "category:"+item.Category {
			continue
		}
		for _, scene := range item.Scenes {
			if scene == q.Scene {
				if unit != "" && unit != item.Unit {
					return nil, "", ErrAnalysisQuery
				}
				unit = item.Unit
				selected[item.ID] = true
				for _, alias := range item.Aliases {
					selected[alias] = true
				}
			}
		}
	}
	if len(selected) == 0 {
		return nil, "", ErrAnalysisQuery
	}
	return selected, unit, nil
}

// Analysis reads consistent raw events in bounded batches. A category occurrence
// counts an event once, even when multiple matching items/tags occur in that event.
// Zero drops are added before any target-item filtering, preserving denominators.
func (s *Service) Analysis(ctx context.Context, q AnalysisQuery) (AnalysisResult, error) {
	items, err := s.CatalogItems(ctx)
	if err != nil {
		return AnalysisResult{}, err
	}
	selected, unit, err := analysisTargetItems(q, items)
	result := AnalysisResult{Groups: []AnalysisGroup{}, Unit: unit}
	if err != nil {
		return result, err
	}
	groups := map[string]*AnalysisGroup{}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var mappingRows []v3db.TemplateItemMapping
		if err := tx.Find(&mappingRows).Error; err != nil {
			return err
		}
		mappings := map[string]v3db.TemplateItemMapping{}
		for _, row := range mappingRows {
			mappings[row.UnknownID] = row
		}
		var commissionRows []v3db.CommissionCatalog
		if err := tx.Find(&commissionRows).Error; err != nil {
			return err
		}
		commissions := map[string]v3db.CommissionCatalog{}
		for _, row := range commissionRows {
			commissions[row.Comm] = row
		}
		lastID := ""
		for {
			base := tx.Where(clause.Eq{Column: "scene", Value: q.Scene}, clause.Gte{Column: "collected_at", Value: q.From}, clause.Lt{Column: "collected_at", Value: q.To})
			if q.Server != "" {
				base = base.Where(clause.Eq{Column: "server", Value: q.Server})
			}
			if q.Series > 0 {
				base = base.Where(clause.Eq{Column: "series", Value: q.Series})
			}
			if lastID != "" {
				base = base.Where(clause.Gt{Column: "record_id", Value: lastID})
			}
			var events []v3db.DropEvent
			if err := base.Order(database.Asc("record_id")).Limit(1000).Find(&events).Error; err != nil {
				return err
			}
			if len(events) == 0 {
				break
			}
			ids := make([]any, 0, len(events))
			for _, event := range events {
				ids = append(ids, event.RecordID)
			}
			type amounts struct{ total, bonus, other int64 }
			byID := map[string]amounts{}
			add := func(record, item, tag string, amount int64) {
				if !selected[item] || !IsPublicItem(item) {
					return
				}
				value := byID[record]
				value.total += amount
				switch tag {
				case "bonus":
					value.bonus += amount
				case "catchup", "event":
					value.other += amount
				}
				byID[record] = value
			}
			if q.Scene == v3db.SceneResearch {
				var items []v3db.ResearchItem
				if err := tx.Where(clause.IN{Column: "record_id", Values: ids}).Find(&items).Error; err != nil {
					return err
				}
				for _, item := range resolveResearchItems(items, mappings) {
					add(item.RecordID, item.Item, item.Tag, item.Amount)
				}
			} else {
				var items []v3db.CommissionItem
				if err := tx.Where(clause.IN{Column: "record_id", Values: ids}).Find(&items).Error; err != nil {
					return err
				}
				for _, item := range resolveCommissionItems(items, mappings) {
					add(item.RecordID, item.Item, item.Tag, item.Amount)
				}
			}
			for _, event := range events {
				meta := eventMetadata(event, commissions)
				if (q.Duration > 0 && meta.Duration != q.Duration) || (q.Category != "" && meta.Category != q.Category) {
					continue
				}
				id, name := meta.ID, meta.Name
				switch q.GroupBy {
				case "series":
					id, name = strconv.Itoa(event.Series), fmt.Sprintf("第 %d 期", event.Series)
				case "category":
					id, name = meta.Category, meta.Category
				case "duration":
					id, name = strconv.Itoa(meta.Duration), fmt.Sprintf("%g 小时", float64(meta.Duration)/3600)
					if meta.Duration == 0 {
						name = "时长未知"
					}
				}
				group := groups[id]
				if group == nil {
					group = &AnalysisGroup{ID: id, Name: name, Series: meta.Series, Category: meta.Category, Duration: meta.Duration, Costs: meta.Costs, Condition: meta.Condition}
					if q.GroupBy != "project" {
						group.Series, group.Category, group.Duration, group.Costs, group.Condition = 0, "", 0, nil, ""
					}
					groups[id] = group
				}
				amount := byID[event.RecordID]
				group.add(amount.total, amount.bonus, amount.other, meta.Duration, event.Scene == v3db.SceneCommission && event.Status == 1)
			}
			lastID = events[len(events)-1].RecordID
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return result, err
	}
	for _, group := range groups {
		if q.Scene == v3db.SceneCommission && group.Samples <= MinCommissionSamples {
			continue
		}
		result.merge(group.AnalysisMetrics)
		group.calculate()
		result.Groups = append(result.Groups, *group)
	}
	result.calculate()
	sort.Slice(result.Groups, func(i, j int) bool { return result.Groups[i].ID < result.Groups[j].ID })
	return result, nil
}

func eventMetadata(event v3db.DropEvent, commissions map[string]v3db.CommissionCatalog) analysisProject {
	if event.Scene == v3db.SceneResearch {
		if meta, ok := catalog.Research[fmt.Sprintf("%d:%s", event.Series, event.Project)]; ok {
			meta.ID, meta.Name = fmt.Sprintf("%d:%s", event.Series, event.Project), event.Project
			return meta
		}
		return analysisProject{ID: fmt.Sprintf("%d:%s", event.Series, event.Project), Name: event.Project, Series: event.Series, Category: strings.Split(event.Project, "-")[0]}
	}
	// Explicit client codes distinguish same-title day/night versions. Only use
	// the title fallback when the catalog considers that title unambiguous.
	meta := commissionMetadata[event.Comm]
	if meta.Name == "" {
		meta = catalog.Commission[event.CommName]
	}
	if meta.Name == "" {
		meta.Category = "unknown"
	}
	meta.ID = event.Comm
	if meta.Name == "" {
		meta.Name = event.CommName
		if meta.Name == "" {
			meta.Name = event.Comm
		}
	}
	if row, ok := commissions[event.Comm]; ok && row.DurationSeconds > 0 {
		meta.Duration = row.DurationSeconds
	}
	return meta
}

func (m *AnalysisMetrics) add(amount, bonus, other int64, duration int, success bool) {
	if m.Samples == 0 || amount < m.Min {
		m.Min = amount
	}
	if amount > m.Max {
		m.Max = amount
	}
	m.Samples++
	m.Amount += amount
	m.ExtraAmount += bonus
	m.OtherAmount += other
	if amount > 0 {
		m.Occurrences++
	}
	if duration > 0 {
		m.TotalSeconds += int64(duration)
	} else {
		m.UnknownDurations++
	}
	if success {
		m.SuccessSamples++
		m.SuccessAmount += amount
	}
}

func (m *AnalysisMetrics) merge(other AnalysisMetrics) {
	if m.Samples == 0 || other.Min < m.Min {
		m.Min = other.Min
	}
	if other.Max > m.Max {
		m.Max = other.Max
	}
	m.Samples += other.Samples
	m.Occurrences += other.Occurrences
	m.Amount += other.Amount
	m.ExtraAmount += other.ExtraAmount
	m.OtherAmount += other.OtherAmount
	m.SuccessSamples += other.SuccessSamples
	m.SuccessAmount += other.SuccessAmount
	m.TotalSeconds += other.TotalSeconds
	m.UnknownDurations += other.UnknownDurations
}

func (m *AnalysisMetrics) calculate() {
	m.PerHour = nil
	if m.Samples > 0 {
		m.DropRate = float64(m.Occurrences) / float64(m.Samples)
		m.Average = float64(m.Amount) / float64(m.Samples)
	}
	if m.TotalSeconds > 0 && m.UnknownDurations == 0 {
		value := float64(m.Amount) * 3600 / float64(m.TotalSeconds)
		m.PerHour = &value
	}
}
