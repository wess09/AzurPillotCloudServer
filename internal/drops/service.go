// Package drops 实现 v3 委托/科研上报、小时聚合与查询。
//
// 只接受 commission 与 research 两类场景；其它统计仍留在客户端本地。
package drops

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"ap-cloud-server/internal/v3db"
)

const (
	AckAccepted         = "accepted"
	AckDuplicate        = "duplicate"
	AckInvalid          = "invalid"
	AckUnsupportedScene = "unsupported_scene"
	AckRetry            = "retry"
)

var (
	recordIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)
	itemRE     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_. -]{0,127}$`)
	tagRE      = regexp.MustCompile(`^[A-Za-z0-9_-]{0,32}$`)
)

var allowedServers = map[string]bool{
	"cn": true, "cn_ios": true, "cn_channel": true,
	"en": true, "jp": true, "tw": true,
}

// Item 是一条奖励。
type Item struct {
	Item   string `json:"item"`
	Tag    string `json:"tag"`
	Amount int64  `json:"amount"`
}

// Drop 是一条委托或科研完成记录。
type Drop struct {
	RecordID         string `json:"record_id"`
	Scene            string `json:"scene"`
	Server           string `json:"server"`
	Status           *int   `json:"status"`
	CollectedAt      string `json:"collected_at"`
	TemplateRevision int64  `json:"template_revision"`
	Client           string `json:"client"`
	DeviceID         string `json:"device_id"`
	Comm             string `json:"comm"`
	Name             string `json:"name"`
	Series           int    `json:"series"`
	Project          string `json:"project"`
	Items            []Item `json:"items"`
}

// Batch 是上报请求体。
type Batch struct {
	Drops []Drop `json:"drops"`
}

// Ack 是逐条回执。
type Ack struct {
	RecordID string `json:"record_id"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
}

type validated struct {
	drop        Drop
	collectedAt time.Time
	status      int16
}

// Service 负责校验与幂等写入。
type Service struct {
	db  *gorm.DB
	now func() time.Time
}

func NewService(db *gorm.DB) *Service {
	return &Service{db: db, now: func() time.Time { return time.Now().UTC() }}
}

// WithClock 供测试注入时钟。
func (s *Service) WithClock(now func() time.Time) *Service {
	s.now = now
	return s
}

// Submit 逐条校验并写入。单条数据库错误只影响该条，回执为 retry。
func (s *Service) Submit(ctx context.Context, list []Drop) []Ack {
	acks := make([]Ack, 0, len(list))
	for _, d := range list {
		v, status, msg := s.validate(d)
		if status != "" {
			acks = append(acks, Ack{RecordID: d.RecordID, Status: status, Error: msg})
			continue
		}
		status, err := s.insertOne(ctx, v)
		ack := Ack{RecordID: v.drop.RecordID, Status: status}
		if err != nil {
			ack.Error = "数据库暂不可用"
		}
		acks = append(acks, ack)
	}
	return acks
}

func (s *Service) validate(d Drop) (validated, string, string) {
	if !recordIDRE.MatchString(d.RecordID) {
		return validated{}, AckInvalid, "record_id 必须是 8–128 位字母、数字、下划线或连字符"
	}
	if d.Scene != v3db.SceneCommission && d.Scene != v3db.SceneResearch {
		return validated{}, AckUnsupportedScene, "仅支持 commission 与 research"
	}
	if !allowedServers[d.Server] {
		return validated{}, AckInvalid, "无效的区服"
	}
	if d.Status == nil || (*d.Status != 0 && *d.Status != 1) {
		return validated{}, AckInvalid, "status 只能为 0 或 1"
	}
	collected, err := time.Parse(time.RFC3339Nano, d.CollectedAt)
	if err != nil {
		return validated{}, AckInvalid, "collected_at 必须是带时区的 RFC3339 时间"
	}
	now := s.now()
	if collected.Before(now.Add(-7*24*time.Hour)) || collected.After(now.Add(7*24*time.Hour)) {
		return validated{}, AckInvalid, "collected_at 超出允许的时钟偏差"
	}
	if len(d.Client) > 64 || len(d.DeviceID) > 64 || len(d.Name) > 128 {
		return validated{}, AckInvalid, "字段长度超限"
	}
	if len(d.Items) == 0 || len(d.Items) > 200 {
		return validated{}, AckInvalid, "items 数量必须在 1–200 之间"
	}
	items := make([]Item, 0, len(d.Items))
	for _, it := range d.Items {
		it.Item = strings.TrimSpace(it.Item)
		it.Tag = strings.TrimSpace(it.Tag)
		if !itemRE.MatchString(it.Item) || it.Amount < 1 || it.Amount > 1_000_000_000 {
			return validated{}, AckInvalid, "物品名称或数量非法"
		}
		if !tagRE.MatchString(it.Tag) {
			return validated{}, AckInvalid, "物品标签非法"
		}
		items = append(items, it)
	}
	d.Items = items
	switch d.Scene {
	case v3db.SceneCommission:
		d.Comm = strings.TrimSpace(d.Comm)
		d.Name = strings.TrimSpace(d.Name)
		if d.Comm == "" || len(d.Comm) > 128 {
			return validated{}, AckInvalid, "委托必须提供 comm"
		}
		d.Series, d.Project = 0, ""
	case v3db.SceneResearch:
		d.Project = strings.TrimSpace(d.Project)
		if d.Series < 1 || d.Series > 100 || d.Project == "" || len(d.Project) > 128 {
			return validated{}, AckInvalid, "科研必须提供 1–100 的 series 与 project"
		}
		d.Comm, d.Name = "", ""
	}
	return validated{drop: d, collectedAt: collected.UTC(), status: int16(*d.Status)}, "", ""
}

func (s *Service) insertOne(ctx context.Context, v validated) (string, error) {
	duplicate := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		ev := v3db.DropEvent{
			RecordID:         v.drop.RecordID,
			Scene:            v.drop.Scene,
			Server:           v.drop.Server,
			Status:           v.status,
			CollectedAt:      v.collectedAt,
			ReceivedAt:       s.now(),
			TemplateRevision: v.drop.TemplateRevision,
			ClientVersion:    v.drop.Client,
			DeviceID:         v.drop.DeviceID,
			Comm:             v.drop.Comm,
			CommName:         v.drop.Name,
			Series:           v.drop.Series,
			Project:          v.drop.Project,
		}
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&ev)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			duplicate = true
			return nil
		}
		switch v.drop.Scene {
		case v3db.SceneCommission:
			rows := make([]v3db.CommissionItem, 0, len(v.drop.Items))
			for _, it := range v.drop.Items {
				rows = append(rows, v3db.CommissionItem{RecordID: ev.RecordID, Item: it.Item, Tag: it.Tag, Amount: it.Amount})
			}
			if err := tx.CreateInBatches(rows, 200).Error; err != nil {
				return err
			}
		case v3db.SceneResearch:
			rows := make([]v3db.ResearchItem, 0, len(v.drop.Items))
			for _, it := range v.drop.Items {
				rows = append(rows, v3db.ResearchItem{RecordID: ev.RecordID, Item: it.Item, Tag: it.Tag, Amount: it.Amount})
			}
			if err := tx.CreateInBatches(rows, 200).Error; err != nil {
				return err
			}
		default:
			return fmt.Errorf("unreachable scene %q", v.drop.Scene)
		}
		return v3db.MarkDirtyHour(tx, ev.CollectedAt)
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return AckRetry, err
		}
		return AckRetry, err
	}
	if duplicate {
		return AckDuplicate, nil
	}
	return AckAccepted, nil
}

// IsPublicItem 过滤分母里的占位符与未映射物品。
func IsPublicItem(item string) bool {
	if strings.EqualFold(strings.TrimSpace(item), "PlaceHolder") {
		return false
	}
	if strings.HasPrefix(item, "U-") || strings.HasPrefix(item, "U?") {
		return false
	}
	return true
}
