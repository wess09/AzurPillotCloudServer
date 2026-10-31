package v3db

import "time"

// TemplateSyncState tracks the external template service. No template images live here.
type TemplateSyncState struct {
	ID           uint8     `gorm:"primaryKey" json:"id"`
	Revision     uint64    `json:"revision"`
	LastSequence uint64    `json:"last_sequence"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (TemplateSyncState) TableName() string { return "template_sync_state" }

// TemplateItemMapping is a published alias replicated from the independent template service.
type TemplateItemMapping struct {
	UnknownID string `gorm:"type:varchar(128);primaryKey" json:"unknown_id"`
	ItemID    string `gorm:"type:varchar(128);not null" json:"item_id"`
	Name      string `gorm:"type:varchar(128);not null" json:"name"`
	Tag       string `gorm:"type:varchar(32);not null" json:"tag"`
	Revision  uint64 `gorm:"not null" json:"revision"`
}

func (TemplateItemMapping) TableName() string { return "template_item_mappings" }

type TemplateItemDefinition struct {
	ID       string   `gorm:"type:varchar(128);primaryKey" json:"id"`
	Name     string   `gorm:"type:varchar(128);not null" json:"name"`
	Category string   `gorm:"type:varchar(32);not null" json:"category"`
	Unit     string   `gorm:"type:varchar(16);not null" json:"unit"`
	Rarity   int      `json:"rarity"`
	Series   int      `json:"series"`
	Version  string   `gorm:"type:varchar(64)" json:"version"`
	Scenes   []string `gorm:"serializer:json;type:text" json:"scenes"`
	GameID   string   `gorm:"type:varchar(64)" json:"game_id"`
	Faction  string   `gorm:"type:varchar(64)" json:"faction"`
	Subtype  string   `gorm:"type:varchar(64)" json:"subtype"`
	Revision uint64   `json:"-"`
}

func (TemplateItemDefinition) TableName() string { return "template_item_definitions" }

type TemplateEventReceipt struct {
	EventID   string    `gorm:"type:varchar(36);primaryKey"`
	BodyHash  string    `gorm:"type:char(64);not null"`
	Sequence  uint64    `gorm:"not null"`
	CreatedAt time.Time `gorm:"not null"`
}

func (TemplateEventReceipt) TableName() string { return "template_event_receipts" }
