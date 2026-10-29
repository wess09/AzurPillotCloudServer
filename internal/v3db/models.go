// Package v3db 定义 v3 委托/科研统计与模板服务器共用的 GORM 模型。
//
// 所有访问均通过 GORM 链式 API 完成，不写裸 SQL。表名显式声明，
// 避免依赖 GORM 的复数推导，便于迁移与运维排查。
package v3db

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

const (
	SceneCommission = "commission"
	SceneResearch   = "research"

	KindFormal    = "formal"
	KindTemporary = "temporary"
)

// JSONB 是 jsonb 列的轻量实现，避免为了一个字段引入整组 datatypes 依赖。
type JSONB []byte

func (j JSONB) Value() (driver.Value, error) {
	if len(j) == 0 {
		return "{}", nil
	}
	return string(j), nil
}

func (j *JSONB) Scan(v any) error {
	switch x := v.(type) {
	case nil:
		*j = nil
	case []byte:
		*j = append((*j)[:0], x...)
	case string:
		*j = append((*j)[:0], x...)
	default:
		return fmt.Errorf("unsupported JSONB value %T", v)
	}
	return nil
}

// MarshalJSON 让 JSONB 在 API 输出中保持原始 JSON，而不是 base64。
func (j JSONB) MarshalJSON() ([]byte, error) {
	if len(j) == 0 {
		return []byte("null"), nil
	}
	if !json.Valid(j) {
		return nil, fmt.Errorf("invalid JSONB payload")
	}
	return j, nil
}

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

// SchemaGuard 用于让多个实例的 AutoMigrate 串行执行。
type SchemaGuard struct {
	ID         uint8     `gorm:"column:id;primaryKey"`
	MigratedAt time.Time `gorm:"column:migrated_at;not null"`
}

func (SchemaGuard) TableName() string { return "schema_guard" }

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

// ContentRelease 记录每次模板发布；manifest 与 catalog 用于计算增量。
type ContentRelease struct {
	Revision        uint64    `gorm:"column:revision;primaryKey;autoIncrement:false"`
	CreatedAt       time.Time `gorm:"column:created_at;not null"`
	Note            string    `gorm:"column:note;type:varchar(256);not null;default:''"`
	ManifestSHA256  string    `gorm:"column:manifest_sha256;type:char(64);not null;default:''"`
	Signature       string    `gorm:"column:signature;type:varchar(128);not null;default:''"`
	MatchThresholds JSONB     `gorm:"column:match_thresholds;type:jsonb;not null;default:'{}'"`
	Catalog         JSONB     `gorm:"column:catalog;type:jsonb;not null;default:'[]'"`
	Deployed        bool      `gorm:"column:deployed;not null;default:false"`
	WorkerURL       string    `gorm:"column:worker_url;type:varchar(256);not null;default:''"`
}

func (ContentRelease) TableName() string { return "content_releases" }

// Template 是模板服务器的当前工作集；发布时冻结为 ContentRelease.Catalog。
type Template struct {
	ID           uint64    `gorm:"column:id;primaryKey;autoIncrement"`
	Key          string    `gorm:"column:key;type:varchar(128);not null;uniqueIndex"`
	Name         string    `gorm:"column:name;type:varchar(128);not null"`
	Item         string    `gorm:"column:item;type:varchar(128);not null;index"`
	Tag          string    `gorm:"column:tag;type:varchar(32);not null;default:''"`
	SceneContext string    `gorm:"column:scene_context;type:varchar(32);not null;default:'all'"`
	Kind         string    `gorm:"column:kind;type:varchar(16);not null;default:'formal'"`
	SHA256       string    `gorm:"column:sha256;type:char(64);not null;index"`
	IconSHA256   string    `gorm:"column:icon_sha256;type:char(64);not null;index"`
	Icon         []byte    `gorm:"column:icon;type:bytea;not null"`
	Enabled      bool      `gorm:"column:enabled;not null;default:true;index"`
	CreatedAt    time.Time `gorm:"column:created_at;not null"`
	UpdatedAt    time.Time `gorm:"column:updated_at;not null"`
}

func (Template) TableName() string { return "templates" }

// TemplateState 是单例发布指针。
type TemplateState struct {
	ID              uint8  `gorm:"column:id;primaryKey"`
	CurrentRevision uint64 `gorm:"column:current_revision;not null;default:0"`
	PublicBase      string `gorm:"column:public_base;type:varchar(256);not null;default:''"`
}

func (TemplateState) TableName() string { return "template_state" }

// UnknownTemplate 是客户端上传后按感知哈希聚合出的未知模板聚类。
// ClusterKey 由感知哈希派生，同一图标的不同上传会落到同一行。
type UnknownTemplate struct {
	ClusterKey  string    `gorm:"column:cluster_key;type:varchar(128);primaryKey"`
	Scene       string    `gorm:"column:scene;type:varchar(32);not null;default:'all'"`
	PHash       string    `gorm:"column:phash;type:char(16);not null;index"`
	SHA256      string    `gorm:"column:sha256;type:char(64);not null;index"`
	Icon        []byte    `gorm:"column:icon;type:bytea;not null"`
	Occurrences int64     `gorm:"column:occurrences;not null;default:0"`
	Status      string    `gorm:"column:status;type:varchar(16);not null;default:'pending';index"`
	Item        string    `gorm:"column:item;type:varchar(128);not null;default:''"`
	Tag         string    `gorm:"column:tag;type:varchar(32);not null;default:''"`
	Source      string    `gorm:"column:source;type:varchar(32);not null;default:'client'"`
	Client      string    `gorm:"column:client;type:varchar(64);not null;default:''"`
	FirstSeenAt time.Time `gorm:"column:first_seen_at;not null"`
	LastSeenAt  time.Time `gorm:"column:last_seen_at;not null"`
}

func (UnknownTemplate) TableName() string { return "unknown_templates" }

// UnknownMapping 记录某次发布中未知聚类到物品的映射；同一聚类可保留多次历史。
type UnknownMapping struct {
	ID         uint64 `gorm:"column:id;primaryKey;autoIncrement"`
	Revision   uint64 `gorm:"column:revision;not null;index"`
	ClusterKey string `gorm:"column:cluster_key;type:varchar(128);not null;index"`
	Item       string `gorm:"column:item;type:varchar(128);not null"`
	Tag        string `gorm:"column:tag;type:varchar(32);not null;default:''"`
}

func (UnknownMapping) TableName() string { return "unknown_mappings" }

type UnknownClusterAlias struct {
	FromKey  string `gorm:"column:from_key;type:varchar(128);primaryKey"`
	ToKey    string `gorm:"column:to_key;type:varchar(128);not null"`
	Revision uint64 `gorm:"column:revision;not null"`
}

func (UnknownClusterAlias) TableName() string { return "unknown_cluster_aliases" }

func AllModels() []any {
	return []any{
		&DropEvent{}, &CommissionItem{}, &ResearchItem{},
		&CommissionHourly{}, &CommissionItemHourly{},
		&ResearchHourly{}, &ResearchItemHourly{},
		&DirtyHour{}, &SchemaGuard{},
		&CommissionCatalog{},
		&ContentRelease{}, &Template{}, &TemplateState{},
		&UnknownTemplate{}, &UnknownMapping{}, &UnknownClusterAlias{},
		&TemplateSyncState{}, &TemplateItemMapping{}, &TemplateEventReceipt{},
	}
}
