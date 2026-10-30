package store

import (
	"gorm.io/datatypes"
	"time"
)

// Persistence models remain separate from API response types.
type Cl1Snapshot struct {
	DeviceID          string    `gorm:"type:text;primaryKey"`
	InstanceID        string    `gorm:"type:text;primaryKey"`
	Month             string    `gorm:"type:text;primaryKey"`
	CollectedAt       time.Time `gorm:"not null"`
	BattleCount       int64     `gorm:"not null"`
	BattleRounds      int64     `gorm:"not null"`
	SortieCost        int64     `gorm:"not null"`
	AkashiEncounters  int64     `gorm:"not null"`
	AkashiAP          int64     `gorm:"not null"`
	AkashiProbability float64   `gorm:"not null"`
	AverageStamina    float64   `gorm:"not null"`
	NetStaminaGain    int64     `gorm:"not null"`
	UpdatedAt         time.Time `gorm:"not null"`
}

func (Cl1Snapshot) TableName() string { return "cl1_snapshots" }

type Cl1Dedup struct {
	RequestID  string         `gorm:"type:text;primaryKey"`
	DeviceID   string         `gorm:"type:text;not null"`
	InstanceID string         `gorm:"type:text;not null"`
	BodyHash   string         `gorm:"type:text;not null"`
	Response   datatypes.JSON `gorm:"type:jsonb;not null"`
	CreatedAt  time.Time      `gorm:"not null"`
	ExpiresAt  time.Time      `gorm:"not null;index:cl1_dedup_expires_idx"`
}

func (Cl1Dedup) TableName() string { return "cl1_dedup" }

type MaintenanceSchedule struct {
	Server    string    `gorm:"type:text;primaryKey"`
	Name      string    `gorm:"type:text;not null"`
	StartsAt  time.Time `gorm:"not null"`
	EndsAt    *time.Time
	UpdatedAt time.Time `gorm:"not null"`
}

func (MaintenanceSchedule) TableName() string { return "maintenance_schedules" }

type OfficialMaintenance struct {
	Server    string `gorm:"type:text;primaryKey"`
	Name      string `gorm:"type:text;not null"`
	StartsAt  *time.Time
	EndsAt    *time.Time
	Title     string    `gorm:"type:text;not null"`
	SourceURL string    `gorm:"type:text;not null"`
	CheckedAt time.Time `gorm:"not null"`
	SyncedAt  *time.Time
	Error     string `gorm:"type:text;not null"`
}

func (OfficialMaintenance) TableName() string { return "maintenance_sources" }

type AnnouncementRecord struct {
	ID        string    `gorm:"type:text;primaryKey"`
	Title     string    `gorm:"type:text;not null"`
	Content   *string   `gorm:"type:text"`
	URL       *string   `gorm:"type:text"`
	Active    bool      `gorm:"not null;index:announcements_active_idx,priority:1"`
	CreatedAt time.Time `gorm:"not null"`
	UpdatedAt time.Time `gorm:"not null;index:announcements_active_idx,priority:2,sort:desc"`
}

func (AnnouncementRecord) TableName() string { return "announcements" }

type UpdatePolicy struct {
	ID          bool      `gorm:"primaryKey;autoIncrement:false"`
	ForceUpdate bool      `gorm:"not null"`
	UpdatedAt   time.Time `gorm:"not null"`
}

func (UpdatePolicy) TableName() string { return "update_policy" }

type PublishHistory struct {
	ID          int64     `gorm:"primaryKey;autoIncrement"`
	Worker      string    `gorm:"type:text;not null;index:publish_history_worker_latest,priority:1"`
	ContentHash string    `gorm:"type:text;not null"`
	Status      string    `gorm:"type:text;not null"`
	Error       *string   `gorm:"type:text"`
	CreatedAt   time.Time `gorm:"not null;index:publish_history_created_idx,sort:desc;index:publish_history_worker_latest,priority:2,sort:desc"`
}

func (PublishHistory) TableName() string { return "publish_history" }

func coreModels() []any {
	return []any{&Cl1Snapshot{}, &Cl1Dedup{}, &MaintenanceSchedule{}, &OfficialMaintenance{}, &AnnouncementRecord{}, &UpdatePolicy{}, &PublishHistory{}}
}
