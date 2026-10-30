package drops

import (
	"context"
	"errors"
	"math"
	"sort"
	"time"

	"ap-cloud-server/internal/database"
	"ap-cloud-server/internal/v3db"
	"gorm.io/gen/field"
	"gorm.io/gorm/clause"
)

// MinCommissionSamples 是公开展示委托统计的最小样本量。
const MinCommissionSamples = 50

var ErrInvalidRange = errors.New("invalid time range")

type CommissionQuery struct {
	From, To time.Time
	Server   string
	Comm     string
	Status   *int
}

type CommissionItemStat struct {
	Item             string   `json:"item"`
	Tag              string   `json:"tag"`
	Occurrences      int64    `json:"occurrences"`
	Amount           int64    `json:"amount"`
	DropRate         float64  `json:"drop_rate"`
	AveragePerSample float64  `json:"average_per_sample"`
	AmountPerHour    *float64 `json:"amount_per_hour"`
}

type CommissionGroup struct {
	Comm            string               `json:"comm"`
	Name            string               `json:"name"`
	Status          int                  `json:"status"`
	Samples         int64                `json:"samples"`
	DurationSeconds int                  `json:"duration_seconds"`
	Items           []CommissionItemStat `json:"items"`
}

type ResearchQuery struct {
	From, To      time.Time
	Server        string
	Series        int
	Project, Item string
	Tag           string
}

type ResearchItemStat struct {
	Item             string   `json:"item"`
	Tag              string   `json:"tag"`
	Occurrences      int64    `json:"occurrences"`
	Amount           int64    `json:"amount"`
	DropRate         float64  `json:"drop_rate"`
	AveragePerSample float64  `json:"average_per_sample"`
	AmountPerHour    *float64 `json:"amount_per_hour"`
}

type ResearchGroup struct {
	Series  int                `json:"series"`
	Project string             `json:"project"`
	Samples int64              `json:"samples"`
	Items   []ResearchItemStat `json:"items"`
}

type CommissionOption struct {
	Comm            string `json:"comm"`
	Name            string `json:"name"`
	Status          int    `json:"status"`
	Samples         int64  `json:"samples"`
	DurationSeconds int    `json:"duration_seconds"`
}

type ResearchOption struct {
	Series  int    `json:"series"`
	Project string `json:"project"`
	Samples int64  `json:"samples"`
}

type commissionSampleRow struct {
	Comm    string
	Status  int16
	Samples int64
}

type commissionItemRow struct {
	Comm        string
	Status      int16
	Item        string
	Tag         string
	Occurrences int64
	Amount      int64
}

func (s *Service) Commission(ctx context.Context, q CommissionQuery) ([]CommissionGroup, error) {
	if !q.To.After(q.From) {
		return nil, ErrInvalidRange
	}
	base := database.Project(s.db.WithContext(ctx).Model(&v3db.CommissionHourly{}), field.NewString("", "comm"), field.NewInt16("", "status"), field.NewInt64("", "samples").Sum().As("samples")).
		Where(clause.Gte{Column: "hour", Value: q.From.UTC()}, clause.Lt{Column: "hour", Value: q.To.UTC()})
	if q.Server != "" {
		base = base.Where(clause.Eq{Column: "server", Value: q.Server})
	}
	if q.Comm != "" {
		base = base.Where(clause.Eq{Column: "comm", Value: q.Comm})
	}
	if q.Status != nil {
		base = base.Where(clause.Eq{Column: "status", Value: *q.Status})
	}
	var samples []commissionSampleRow
	if err := database.Group(base, "comm", "status").
		Having(field.NewInt64("", "samples").Sum().Gt(MinCommissionSamples)).
		Scan(&samples).Error; err != nil {
		return nil, err
	}
	if len(samples) == 0 {
		return []CommissionGroup{}, nil
	}

	commSet := make(map[string]bool, len(samples))
	comms := make([]string, 0, len(samples))
	for _, row := range samples {
		if !commSet[row.Comm] {
			commSet[row.Comm] = true
			comms = append(comms, row.Comm)
		}
	}
	catalogByComm := make(map[string]v3db.CommissionCatalog, len(comms))
	var catalog []v3db.CommissionCatalog
	if err := s.db.WithContext(ctx).Where(field.NewString("", "comm").In(comms...)).Find(&catalog).Error; err != nil {
		return nil, err
	}
	for _, row := range catalog {
		catalogByComm[row.Comm] = row
	}

	itemQ := database.Project(s.db.WithContext(ctx).Model(&v3db.CommissionItemHourly{}), field.NewString("", "comm"), field.NewInt16("", "status"), field.NewString("", "item"), field.NewString("", "tag"), field.NewInt64("", "occurrences").Sum().As("occurrences"), field.NewInt64("", "amount").Sum().As("amount")).
		Where(clause.Gte{Column: "hour", Value: q.From.UTC()}, clause.Lt{Column: "hour", Value: q.To.UTC()})
	if q.Server != "" {
		itemQ = itemQ.Where(clause.Eq{Column: "server", Value: q.Server})
	}
	if q.Comm != "" {
		itemQ = itemQ.Where(clause.Eq{Column: "comm", Value: q.Comm})
	}
	if q.Status != nil {
		itemQ = itemQ.Where(clause.Eq{Column: "status", Value: *q.Status})
	}
	var items []commissionItemRow
	if err := database.Group(itemQ, "comm", "status", "item", "tag").Scan(&items).Error; err != nil {
		return nil, err
	}

	type key struct {
		Comm   string
		Status int16
	}
	groupByKey := make(map[key]*CommissionGroup, len(samples))
	for _, row := range samples {
		cat := catalogByComm[row.Comm]
		name := cat.Name
		if name == "" {
			name = row.Comm
		}
		groupByKey[key{row.Comm, row.Status}] = &CommissionGroup{
			Comm: row.Comm, Name: name, Status: int(row.Status),
			Samples: row.Samples, DurationSeconds: cat.DurationSeconds,
			Items: []CommissionItemStat{},
		}
	}
	for _, row := range items {
		g := groupByKey[key{row.Comm, row.Status}]
		if g == nil {
			continue // 该组样本量未达到展示门槛。
		}
		stat := CommissionItemStat{
			Item: row.Item, Tag: row.Tag,
			Occurrences:      row.Occurrences,
			Amount:           row.Amount,
			DropRate:         round6(float64(row.Occurrences) / float64(g.Samples)),
			AveragePerSample: round6(float64(row.Amount) / float64(g.Samples)),
		}
		if g.DurationSeconds > 0 && g.Samples > 0 {
			perHour := round6(float64(row.Amount) * 3600 / (float64(g.Samples) * float64(g.DurationSeconds)))
			stat.AmountPerHour = &perHour
		}
		g.Items = append(g.Items, stat)
	}

	groups := make([]CommissionGroup, 0, len(groupByKey))
	for _, g := range groupByKey {
		sort.Slice(g.Items, func(i, j int) bool {
			if g.Items[i].Amount != g.Items[j].Amount {
				return g.Items[i].Amount > g.Items[j].Amount
			}
			if g.Items[i].Item != g.Items[j].Item {
				return g.Items[i].Item < g.Items[j].Item
			}
			return g.Items[i].Tag < g.Items[j].Tag
		})
		groups = append(groups, *g)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Samples != groups[j].Samples {
			return groups[i].Samples > groups[j].Samples
		}
		if groups[i].Comm != groups[j].Comm {
			return groups[i].Comm < groups[j].Comm
		}
		return groups[i].Status < groups[j].Status
	})
	return groups, nil
}

type researchSampleRow struct {
	Series  int
	Project string
	Samples int64
}

type researchItemRow struct {
	Series      int
	Project     string
	Item        string
	Tag         string
	Occurrences int64
	Amount      int64
}

func (s *Service) Research(ctx context.Context, q ResearchQuery) ([]ResearchGroup, error) {
	if !q.To.After(q.From) {
		return nil, ErrInvalidRange
	}
	base := database.Project(s.db.WithContext(ctx).Model(&v3db.ResearchHourly{}), field.NewInt("", "series"), field.NewString("", "project"), field.NewInt64("", "samples").Sum().As("samples")).
		Where(clause.Gte{Column: "hour", Value: q.From.UTC()}, clause.Lt{Column: "hour", Value: q.To.UTC()})
	if q.Server != "" {
		base = base.Where(clause.Eq{Column: "server", Value: q.Server})
	}
	if q.Series > 0 {
		base = base.Where(clause.Eq{Column: "series", Value: q.Series})
	}
	if q.Project != "" {
		base = base.Where(clause.Eq{Column: "project", Value: q.Project})
	}
	var samples []researchSampleRow
	if err := database.Group(base, "series", "project").Scan(&samples).Error; err != nil {
		return nil, err
	}
	if len(samples) == 0 {
		return []ResearchGroup{}, nil
	}
	type researchGroupKey struct {
		Series  int
		Project string
	}
	groupByKey := make(map[researchGroupKey]*ResearchGroup, len(samples))
	for _, row := range samples {
		groupByKey[researchGroupKey{row.Series, row.Project}] = &ResearchGroup{
			Series: row.Series, Project: row.Project, Samples: row.Samples,
			Items: []ResearchItemStat{},
		}
	}
	itemQ := database.Project(s.db.WithContext(ctx).Model(&v3db.ResearchItemHourly{}), field.NewInt("", "series"), field.NewString("", "project"), field.NewString("", "item"), field.NewString("", "tag"), field.NewInt64("", "occurrences").Sum().As("occurrences"), field.NewInt64("", "amount").Sum().As("amount")).
		Where(clause.Gte{Column: "hour", Value: q.From.UTC()}, clause.Lt{Column: "hour", Value: q.To.UTC()})
	if q.Server != "" {
		itemQ = itemQ.Where(clause.Eq{Column: "server", Value: q.Server})
	}
	if q.Series > 0 {
		itemQ = itemQ.Where(clause.Eq{Column: "series", Value: q.Series})
	}
	if q.Project != "" {
		itemQ = itemQ.Where(clause.Eq{Column: "project", Value: q.Project})
	}
	if q.Item != "" {
		itemQ = itemQ.Where(clause.Eq{Column: "item", Value: q.Item})
	}
	if q.Tag != "" {
		itemQ = itemQ.Where(clause.Eq{Column: "tag", Value: q.Tag})
	}
	var items []researchItemRow
	if err := database.Group(itemQ, "series", "project", "item", "tag").Scan(&items).Error; err != nil {
		return nil, err
	}
	for _, row := range items {
		g := groupByKey[researchGroupKey{row.Series, row.Project}]
		if g == nil {
			continue
		}
		g.Items = append(g.Items, ResearchItemStat{
			Item: row.Item, Tag: row.Tag,
			Occurrences:      row.Occurrences,
			Amount:           row.Amount,
			DropRate:         round6(float64(row.Occurrences) / float64(g.Samples)),
			AveragePerSample: round6(float64(row.Amount) / float64(g.Samples)),
		})
	}
	groups := make([]ResearchGroup, 0, len(groupByKey))
	for _, g := range groupByKey {
		sort.Slice(g.Items, func(i, j int) bool {
			if g.Items[i].Amount != g.Items[j].Amount {
				return g.Items[i].Amount > g.Items[j].Amount
			}
			if g.Items[i].Item != g.Items[j].Item {
				return g.Items[i].Item < g.Items[j].Item
			}
			return g.Items[i].Tag < g.Items[j].Tag
		})
		groups = append(groups, *g)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Samples != groups[j].Samples {
			return groups[i].Samples > groups[j].Samples
		}
		if groups[i].Series != groups[j].Series {
			return groups[i].Series < groups[j].Series
		}
		return groups[i].Project < groups[j].Project
	})
	return groups, nil
}

func (s *Service) CommissionOptions(ctx context.Context, from, to time.Time, server string) ([]CommissionOption, error) {
	if !to.After(from) {
		return nil, ErrInvalidRange
	}
	q := database.Project(s.db.WithContext(ctx).Model(&v3db.CommissionHourly{}), field.NewString("", "comm"), field.NewInt16("", "status"), field.NewInt64("", "samples").Sum().As("samples")).
		Where(clause.Gte{Column: "hour", Value: from.UTC()}, clause.Lt{Column: "hour", Value: to.UTC()})
	if server != "" {
		q = q.Where(clause.Eq{Column: "server", Value: server})
	}
	var rows []commissionSampleRow
	if err := database.Group(q, "comm", "status").Order(database.Asc("comm")).Order(database.Asc("status")).Scan(&rows).Error; err != nil {
		return nil, err
	}
	comms := make([]string, 0, len(rows))
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		if !seen[row.Comm] {
			seen[row.Comm] = true
			comms = append(comms, row.Comm)
		}
	}
	catalogByComm := make(map[string]v3db.CommissionCatalog, len(comms))
	if len(comms) > 0 {
		var catalog []v3db.CommissionCatalog
		if err := s.db.WithContext(ctx).Where(field.NewString("", "comm").In(comms...)).Find(&catalog).Error; err != nil {
			return nil, err
		}
		for _, row := range catalog {
			catalogByComm[row.Comm] = row
		}
	}
	out := make([]CommissionOption, 0, len(rows))
	for _, row := range rows {
		cat := catalogByComm[row.Comm]
		name := cat.Name
		if name == "" {
			name = row.Comm
		}
		out = append(out, CommissionOption{
			Comm: row.Comm, Name: name, Status: int(row.Status),
			Samples: row.Samples, DurationSeconds: cat.DurationSeconds,
		})
	}
	return out, nil
}

func (s *Service) ResearchOptions(ctx context.Context, from, to time.Time, server string) ([]ResearchOption, error) {
	if !to.After(from) {
		return nil, ErrInvalidRange
	}
	q := database.Project(s.db.WithContext(ctx).Model(&v3db.ResearchHourly{}), field.NewInt("", "series"), field.NewString("", "project"), field.NewInt64("", "samples").Sum().As("samples")).
		Where(clause.Gte{Column: "hour", Value: from.UTC()}, clause.Lt{Column: "hour", Value: to.UTC()})
	if server != "" {
		q = q.Where(clause.Eq{Column: "server", Value: server})
	}
	var rows []researchSampleRow
	if err := database.Group(q, "series", "project").Order(database.Asc("series")).Order(database.Asc("project")).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]ResearchOption, 0, len(rows))
	for _, row := range rows {
		out = append(out, ResearchOption(row))
	}
	return out, nil
}

func round6(v float64) float64 {
	return math.Round(v*1e6) / 1e6
}
