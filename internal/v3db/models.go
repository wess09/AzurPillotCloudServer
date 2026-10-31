// Package v3db 定义 主服务委托/科研统计的 GORM 模型。
//
// 所有访问均通过 GORM 链式 API 完成，不写裸 SQL。表名显式声明，
// 避免依赖 GORM 的复数推导，便于迁移与运维排查。
package v3db

import "time"

const (
	SceneCommission = "commission"
	SceneResearch   = "research"
)

// DropEvent 是一条委托或科研完成记录；record_id 全局唯一，作为幂等键。
type DropEvent struct {
	RecordID         string    `gorm:"column:record_id;type:varchar(128);primaryKey"`
	Scene            string    `gorm:"column:scene;type:varchar(16);not null;index:idx_drop_events_scene_time,priority:1"`
	Server           string    `gorm:"column:server;type:varchar(32);not null;index:idx_drop_events_scene_time,priority:2"`
	Status           int16     `gorm:"column:status;not null"`
	CollectedAt      time.Time `gorm:"column:collected_at;not null;index:idx_drop_events_scene_time,priority:3"`
	ReceivedAt       time.Time `gorm:"column:received_at;not null"`
	TemplateRevision int64     `gorm:"column:template_revision;not null;default:0"`
	ClientVersion    string    `gorm:"column:client_version;type:varchar(64);not null;default:''"`
	DeviceID         string    `gorm:"column:device_id;type:varchar(64);not null;default:''"`
	Review           string    `gorm:"column:review;type:varchar(32);not null;default:''"`
	// Comm / CommName 仅委托使用；Series / Project 仅科研使用。
	Comm     string `gorm:"column:comm;type:varchar(128);not null;default:''"`
	CommName string `gorm:"column:comm_name;type:varchar(128);not null;default:''"`
	Series   int    `gorm:"column:series;not null;default:0"`
	Project  string `gorm:"column:project;type:varchar(128);not null;default:''"`
}

func (DropEvent) TableName() string { return "drop_events" }

// CommissionItem 是委托奖励行。comm/status 通过 record_id 关联 drop_events，
// 聚合时按 record_id 连接，避免在每行重复存储。
type CommissionItem struct {
	ID       uint64 `gorm:"column:id;primaryKey;autoIncrement"`
	RecordID string `gorm:"column:record_id;type:varchar(128);not null;index"`
	Item     string `gorm:"column:item;type:varchar(128);not null"`
	Tag      string `gorm:"column:tag;type:varchar(32);not null;default:''"`
	Amount   int64  `gorm:"column:amount;not null"`
}

func (CommissionItem) TableName() string { return "commission_items" }

// ResearchItem 是科研完成奖励行。
type ResearchItem struct {
	ID       uint64 `gorm:"column:id;primaryKey;autoIncrement"`
	RecordID string `gorm:"column:record_id;type:varchar(128);not null;index"`
	Item     string `gorm:"column:item;type:varchar(128);not null"`
	Tag      string `gorm:"column:tag;type:varchar(32);not null;default:''"`
	Amount   int64  `gorm:"column:amount;not null"`
}

func (ResearchItem) TableName() string { return "research_items" }

// 小时聚合表。hour 为 UTC 整点。

type CommissionHourly struct {
	ID      uint64    `gorm:"column:id;primaryKey;autoIncrement"`
	Hour    time.Time `gorm:"column:hour;not null;uniqueIndex:uq_commission_hourly,priority:1"`
	Server  string    `gorm:"column:server;type:varchar(32);not null;uniqueIndex:uq_commission_hourly,priority:2"`
	Comm    string    `gorm:"column:comm;type:varchar(128);not null;uniqueIndex:uq_commission_hourly,priority:3"`
	Status  int16     `gorm:"column:status;not null;uniqueIndex:uq_commission_hourly,priority:4"`
	Samples int64     `gorm:"column:samples;not null;default:0"`
}

func (CommissionHourly) TableName() string { return "commission_hourly" }

type CommissionItemHourly struct {
	ID          uint64    `gorm:"column:id;primaryKey;autoIncrement"`
	Hour        time.Time `gorm:"column:hour;not null;uniqueIndex:uq_commission_item_hourly,priority:1"`
	Server      string    `gorm:"column:server;type:varchar(32);not null;uniqueIndex:uq_commission_item_hourly,priority:2"`
	Comm        string    `gorm:"column:comm;type:varchar(128);not null;uniqueIndex:uq_commission_item_hourly,priority:3"`
	Status      int16     `gorm:"column:status;not null;uniqueIndex:uq_commission_item_hourly,priority:4"`
	Item        string    `gorm:"column:item;type:varchar(128);not null;uniqueIndex:uq_commission_item_hourly,priority:5"`
	Tag         string    `gorm:"column:tag;type:varchar(32);not null;default:'';uniqueIndex:uq_commission_item_hourly,priority:6"`
	Occurrences int64     `gorm:"column:occurrences;not null;default:0"`
	Amount      int64     `gorm:"column:amount;not null;default:0"`
}

func (CommissionItemHourly) TableName() string { return "commission_item_hourly" }

type ResearchHourly struct {
	ID      uint64    `gorm:"column:id;primaryKey;autoIncrement"`
	Hour    time.Time `gorm:"column:hour;not null;uniqueIndex:uq_research_hourly,priority:1"`
	Server  string    `gorm:"column:server;type:varchar(32);not null;uniqueIndex:uq_research_hourly,priority:2"`
	Series  int       `gorm:"column:series;not null;uniqueIndex:uq_research_hourly,priority:3"`
	Project string    `gorm:"column:project;type:varchar(128);not null;uniqueIndex:uq_research_hourly,priority:4"`
	Samples int64     `gorm:"column:samples;not null;default:0"`
}

func (ResearchHourly) TableName() string { return "research_hourly" }

type ResearchItemHourly struct {
	ID          uint64    `gorm:"column:id;primaryKey;autoIncrement"`
	Hour        time.Time `gorm:"column:hour;not null;uniqueIndex:uq_research_item_hourly,priority:1"`
	Server      string    `gorm:"column:server;type:varchar(32);not null;uniqueIndex:uq_research_item_hourly,priority:2"`
	Series      int       `gorm:"column:series;not null;uniqueIndex:uq_research_item_hourly,priority:3"`
	Project     string    `gorm:"column:project;type:varchar(128);not null;uniqueIndex:uq_research_item_hourly,priority:4"`
	Item        string    `gorm:"column:item;type:varchar(128);not null;uniqueIndex:uq_research_item_hourly,priority:5"`
	Tag         string    `gorm:"column:tag;type:varchar(32);not null;default:'';uniqueIndex:uq_research_item_hourly,priority:6"`
	Occurrences int64     `gorm:"column:occurrences;not null;default:0"`
	Amount      int64     `gorm:"column:amount;not null;default:0"`
}

func (ResearchItemHourly) TableName() string { return "research_item_hourly" }

// DirtyHour 标记需要重算的 UTC 小时。写入侧只 INSERT，聚合侧按高水位删除。
type DirtyHour struct {
	ID        uint64    `gorm:"column:id;primaryKey;autoIncrement"`
	Hour      time.Time `gorm:"column:hour;not null;index"`
	CreatedAt time.Time `gorm:"column:created_at;not null"`
}

func (DirtyHour) TableName() string { return "aggregation_dirty_hours" }

// CommissionCatalog 提供委托时长，用于 amount_per_hour。
type CommissionCatalog struct {
	Comm              string `gorm:"column:comm;type:varchar(128);primaryKey"`
	Name              string `gorm:"column:name;type:varchar(128);not null;default:''"`
	DurationSeconds   int    `gorm:"column:duration_seconds;not null;default:0"`
	ExpirationSeconds int    `gorm:"column:expiration_seconds;not null;default:0"`
	Code              string `gorm:"column:code;type:varchar(32);not null;default:''"`
	Active            bool   `gorm:"column:active;not null;default:true"`
}

func (CommissionCatalog) TableName() string { return "commission_catalog" }

// AllModels declares only main-service statistics and small mapping/status metadata.
func AllModels() []any {
	return []any{&DropEvent{}, &CommissionItem{}, &ResearchItem{},
		&CommissionHourly{}, &CommissionItemHourly{}, &ResearchHourly{}, &ResearchItemHourly{},
		&DirtyHour{}, &CommissionCatalog{}, &TemplateSyncState{}, &TemplateItemMapping{}, &TemplateEventReceipt{}, &TemplateItemDefinition{}}
}
