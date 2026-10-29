package drops

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"ap-cloud-server/internal/v3db"
)

// Aggregator 把脏小时重算为四张小时聚合表。
type Aggregator struct {
	db       *gorm.DB
	interval time.Duration
	logger   *slog.Logger
	wake     <-chan struct{}
}

func (a *Aggregator) WithWake(wake <-chan struct{}) *Aggregator {
	a.wake = wake
	return a
}

func NewAggregator(db *gorm.DB, interval time.Duration, logger *slog.Logger) *Aggregator {
	if interval < time.Minute {
		interval = time.Hour
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Aggregator{db: db, interval: interval, logger: logger}
}

// Run 启动时先跑一次，之后按间隔运行；ctx 取消后退出。
func (a *Aggregator) Run(ctx context.Context) {
	if err := a.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
		a.logger.Warn("v3 初始聚合失败", "err", err)
	}
	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.wake:
			if err := a.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				a.logger.Warn("模板映射后的历史聚合失败", "err", err)
			}
		case <-ticker.C:
			if err := a.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				a.logger.Warn("v3 聚合失败", "err", err)
			}
		}
	}
}

type dirtyGroup struct {
	Hour  time.Time
	MaxID uint64
}

// RunOnce 处理当前所有脏小时。每个小时独立事务；单小时失败不影响其它小时。
func (a *Aggregator) RunOnce(ctx context.Context) error {
	var groups []dirtyGroup
	if err := a.db.WithContext(ctx).Model(&v3db.DirtyHour{}).
		Select("hour, MAX(id) AS max_id").
		Group("hour").
		Order("hour ASC").
		Scan(&groups).Error; err != nil {
		return err
	}
	var errs []error
	for _, g := range groups {
		if err := a.aggregateHour(ctx, g.Hour.UTC().Truncate(time.Hour), g.MaxID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (a *Aggregator) aggregateHour(ctx context.Context, hour time.Time, maxID uint64) error {
	return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 先锁住本小时的高水位标记。其它实例若已处理并删除该行，直接跳过。
		var lock v3db.DirtyHour
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", maxID).Take(&lock).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}

		end := hour.Add(time.Hour)
		var events []v3db.DropEvent
		if err := tx.Where("collected_at >= ? AND collected_at < ?", hour, end).Find(&events).Error; err != nil {
			return err
		}

		// 只读取本小时事件对应的物品行，避免全表扫描。
		eventIDs := tx.Model(&v3db.DropEvent{}).Select("record_id").
			Where("collected_at >= ? AND collected_at < ?", hour, end)
		var commissionItems []v3db.CommissionItem
		if err := tx.Where("record_id IN (?)", eventIDs).Find(&commissionItems).Error; err != nil {
			return err
		}
		var researchItems []v3db.ResearchItem
		if err := tx.Where("record_id IN (?)", eventIDs).Find(&researchItems).Error; err != nil {
			return err
		}

		var mappings []v3db.TemplateItemMapping
		if err := tx.Find(&mappings).Error; err != nil {
			return err
		}
		aliases := make(map[string]v3db.TemplateItemMapping, len(mappings))
		for _, mapping := range mappings {
			aliases[mapping.UnknownID] = mapping
		}
		commissionItems = resolveCommissionItems(commissionItems, aliases)
		researchItems = resolveResearchItems(researchItems, aliases)
		commissionSamples, commissionRows := aggregateCommission(hour, events, commissionItems)
		researchSamples, researchRows := aggregateResearch(hour, events, researchItems)

		if err := tx.Where("hour = ?", hour).Delete(&v3db.CommissionHourly{}).Error; err != nil {
			return err
		}
		if err := tx.Where("hour = ?", hour).Delete(&v3db.CommissionItemHourly{}).Error; err != nil {
			return err
		}
		if err := tx.Where("hour = ?", hour).Delete(&v3db.ResearchHourly{}).Error; err != nil {
			return err
		}
		if err := tx.Where("hour = ?", hour).Delete(&v3db.ResearchItemHourly{}).Error; err != nil {
			return err
		}
		if len(commissionSamples) > 0 {
			if err := tx.CreateInBatches(commissionSamples, 500).Error; err != nil {
				return err
			}
		}
		if len(commissionRows) > 0 {
			if err := tx.CreateInBatches(commissionRows, 500).Error; err != nil {
				return err
			}
		}
		if len(researchSamples) > 0 {
			if err := tx.CreateInBatches(researchSamples, 500).Error; err != nil {
				return err
			}
		}
		if len(researchRows) > 0 {
			if err := tx.CreateInBatches(researchRows, 500).Error; err != nil {
				return err
			}
		}
		return tx.Where("id <= ?", maxID).Delete(&v3db.DirtyHour{}).Error
	})
}

type commissionKey struct {
	Server string
	Comm   string
	Status int16
}

type commissionItemKey struct {
	Server string
	Comm   string
	Status int16
	Item   string
	Tag    string
}

func aggregateCommission(hour time.Time, events []v3db.DropEvent, items []v3db.CommissionItem) ([]v3db.CommissionHourly, []v3db.CommissionItemHourly) {
	byID := make(map[string]v3db.DropEvent, len(events))
	samples := make(map[commissionKey]int64)
	for _, ev := range events {
		if ev.Scene != v3db.SceneCommission {
			continue
		}
		byID[ev.RecordID] = ev
		samples[commissionKey{ev.Server, ev.Comm, ev.Status}]++
	}
	type itemAgg struct{ occurrences, amount int64 }
	itemSums := make(map[commissionItemKey]*itemAgg)
	for _, it := range items {
		ev, ok := byID[it.RecordID]
		if !ok || !IsPublicItem(it.Item) {
			continue
		}
		k := commissionItemKey{ev.Server, ev.Comm, ev.Status, it.Item, it.Tag}
		v := itemSums[k]
		if v == nil {
			v = &itemAgg{}
			itemSums[k] = v
		}
		v.occurrences++
		v.amount += it.Amount
	}
	sampleRows := make([]v3db.CommissionHourly, 0, len(samples))
	for k, n := range samples {
		sampleRows = append(sampleRows, v3db.CommissionHourly{Hour: hour, Server: k.Server, Comm: k.Comm, Status: k.Status, Samples: n})
	}
	itemRows := make([]v3db.CommissionItemHourly, 0, len(itemSums))
	for k, v := range itemSums {
		itemRows = append(itemRows, v3db.CommissionItemHourly{
			Hour: hour, Server: k.Server, Comm: k.Comm, Status: k.Status,
			Item: k.Item, Tag: k.Tag, Occurrences: v.occurrences, Amount: v.amount,
		})
	}
	return sampleRows, itemRows
}

type researchKey struct {
	Server  string
	Series  int
	Project string
}

type researchItemKey struct {
	Server  string
	Series  int
	Project string
	Item    string
	Tag     string
}

func aggregateResearch(hour time.Time, events []v3db.DropEvent, items []v3db.ResearchItem) ([]v3db.ResearchHourly, []v3db.ResearchItemHourly) {
	byID := make(map[string]v3db.DropEvent, len(events))
	samples := make(map[researchKey]int64)
	for _, ev := range events {
		if ev.Scene != v3db.SceneResearch {
			continue
		}
		byID[ev.RecordID] = ev
		samples[researchKey{ev.Server, ev.Series, ev.Project}]++
	}
	type itemAgg struct{ occurrences, amount int64 }
	itemSums := make(map[researchItemKey]*itemAgg)
	for _, it := range items {
		ev, ok := byID[it.RecordID]
		if !ok || !IsPublicItem(it.Item) {
			continue
		}
		k := researchItemKey{ev.Server, ev.Series, ev.Project, it.Item, it.Tag}
		v := itemSums[k]
		if v == nil {
			v = &itemAgg{}
			itemSums[k] = v
		}
		v.occurrences++
		v.amount += it.Amount
	}
	sampleRows := make([]v3db.ResearchHourly, 0, len(samples))
	for k, n := range samples {
		sampleRows = append(sampleRows, v3db.ResearchHourly{Hour: hour, Server: k.Server, Series: k.Series, Project: k.Project, Samples: n})
	}
	itemRows := make([]v3db.ResearchItemHourly, 0, len(itemSums))
	for k, v := range itemSums {
		itemRows = append(itemRows, v3db.ResearchItemHourly{
			Hour: hour, Server: k.Server, Series: k.Series, Project: k.Project,
			Item: k.Item, Tag: k.Tag, Occurrences: v.occurrences, Amount: v.amount,
		})
	}
	return sampleRows, itemRows
}
