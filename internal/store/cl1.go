package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"ap-cloud-server/internal/database"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Cl1Outcome 是 CL1 提交事务的业务结果。
type Cl1Outcome int

const (
	// Cl1Accepted 表示快照与去重记录都已提交成功。
	Cl1Accepted Cl1Outcome = iota
	// Cl1Duplicate 表示同一 request_id 同一正文的重复请求，
	// Payload 中携带原业务结果。
	Cl1Duplicate
	// Cl1RequestConflict 表示同一 request_id 携带不同正文。
	Cl1RequestConflict
	// Cl1StaleSnapshot 表示旧快照不能覆盖新快照。
	Cl1StaleSnapshot
)

// Cl1Record 是一次 CL1 月度快照写入。
type Cl1Record struct {
	Legacy            bool
	RequestID         string
	DeviceID          string
	InstanceID        string
	Month             string
	CollectedAt       time.Time
	BattleCount       int64
	BattleRounds      int64
	SortieCost        int64
	AkashiEncounters  int64
	AkashiAP          int64
	AkashiProbability float64
	AverageStamina    float64
	NetStaminaGain    int64
}

// SubmitCl1 在单个事务里完成快照覆盖写与请求去重登记。
//
// 语义（重构计划 3.5）：
//   - 按 (device_id, instance_id, month) 覆盖更新，不累加；
//   - 旧快照（collected_at 更早）不能覆盖新快照 → Cl1StaleSnapshot；
//   - 同 request_id 同正文重试 → Cl1Duplicate，返回原业务结果；
//   - 同 request_id 不同正文 → Cl1RequestConflict；
//   - responseJSON 是 duplicate=false 的完整成功响应体，
//     首次成功时与去重记录一起原子提交。
//
// 返回 (结果, 需要返回给客户端的响应体, error)。
// Reserving the request ID first serializes concurrent retries before any
// snapshot mutation. A stale snapshot rolls back the reservation as well.
func (s *Store) SubmitCl1(ctx context.Context, rec Cl1Record, bodyHash string, responseJSON []byte) (Cl1Outcome, []byte, error) {
	outcome, payload := Cl1Accepted, responseJSON
	stale := errors.New("stale snapshot")
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		dedup := Cl1Dedup{RequestID: rec.RequestID, DeviceID: rec.DeviceID, InstanceID: rec.InstanceID, BodyHash: bodyHash, Response: datatypes.JSON(responseJSON), ExpiresAt: time.Now().UTC().Add(s.DedupTTL)}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&dedup)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			var previous Cl1Dedup
			if err := tx.Take(&previous, clause.Eq{Column: "request_id", Value: rec.RequestID}).Error; err != nil {
				return err
			}
			if previous.BodyHash == bodyHash {
				outcome, payload = Cl1Duplicate, []byte(previous.Response)
			} else {
				outcome, payload = Cl1RequestConflict, nil
			}
			return nil
		}
		row := Cl1Snapshot{DeviceID: rec.DeviceID, InstanceID: rec.InstanceID, Month: rec.Month, CollectedAt: rec.CollectedAt, BattleCount: rec.BattleCount, BattleRounds: rec.BattleRounds, SortieCost: rec.SortieCost, AkashiEncounters: rec.AkashiEncounters, AkashiAP: rec.AkashiAP, AkashiProbability: rec.AkashiProbability, AverageStamina: rec.AverageStamina, NetStaminaGain: rec.NetStaminaGain}
		conditions := []clause.Expression{clause.Lte{Column: clause.Column{Table: row.TableName(), Name: "collected_at"}, Value: clause.Column{Table: "excluded", Name: "collected_at"}}}
		if rec.Legacy {
			for _, column := range []string{"battle_count", "akashi_encounters", "akashi_ap"} {
				conditions = append(conditions, clause.Lte{Column: clause.Column{Table: row.TableName(), Name: column}, Value: clause.Column{Table: "excluded", Name: column}})
			}
		}
		result = tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "device_id"}, {Name: "instance_id"}, {Name: "month"}},
			DoUpdates: clause.AssignmentColumns([]string{"collected_at", "battle_count", "battle_rounds", "sortie_cost", "akashi_encounters", "akashi_ap", "akashi_probability", "average_stamina", "net_stamina_gain", "updated_at"}),
			Where:     clause.Where{Exprs: conditions},
		}).Create(&row)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return stale
		}
		return nil
	})
	if errors.Is(err, stale) {
		return Cl1StaleSnapshot, nil, nil
	}
	if err != nil {
		return 0, nil, fmt.Errorf("保存 CL1 事务失败: %w", err)
	}
	return outcome, payload, nil
}

// Cl1Row 是一条已存储的月度快照。
type Cl1Row struct {
	DeviceID          string
	InstanceID        string
	Month             string
	CollectedAt       time.Time
	BattleCount       int64
	AkashiEncounters  int64
	AkashiAP          int64
	AkashiProbability float64
	AverageStamina    float64
	UpdatedAt         time.Time
}

// ListCl1Snapshots 返回指定设备的月度快照（按更新时间倒序）。
func (s *Store) ListCl1Snapshots(ctx context.Context, deviceID, month string, limit int) ([]Cl1Row, error) {
	var rows []Cl1Snapshot
	if err := s.DB.WithContext(ctx).Where(clause.Eq{Column: "device_id", Value: deviceID}, clause.Eq{Column: "month", Value: month}).Order(database.Desc("updated_at")).Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]Cl1Row, 0, len(rows))
	for _, r := range rows {
		out = append(out, Cl1Row{DeviceID: r.DeviceID, InstanceID: r.InstanceID, Month: r.Month, CollectedAt: r.CollectedAt, BattleCount: r.BattleCount, AkashiEncounters: r.AkashiEncounters, AkashiAP: r.AkashiAP, AkashiProbability: r.AkashiProbability, AverageStamina: r.AverageStamina, UpdatedAt: r.UpdatedAt})
	}
	return out, nil
}
