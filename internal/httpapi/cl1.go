package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"ap-cloud-server/internal/store"
)

// cl1MaxBodyBytes 是 CL1 请求体的上限（计划 3.5：64 KiB）。
const cl1MaxBodyBytes = 64 << 10

var (
	deviceIDPattern   = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)
	instanceIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{16}$`)
	monthPattern      = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)
	uuidPattern       = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// cl1AllowedFields 是 CL1 请求允许的全部字段；任何其他字段都拒绝。
var cl1AllowedFields = map[string]bool{
	"request_id":        true,
	"device_id":         true,
	"instance_id":       true,
	"month":             true,
	"collected_at":      true,
	"battle_count":      true,
	"akashi_encounters": true,
	"akashi_ap":         true,
}

// cl1Request 是 CL1 请求正文。字段与计划 3.5 一一对应。
type cl1Request struct {
	RequestID        string `json:"request_id"`
	DeviceID         string `json:"device_id"`
	InstanceID       string `json:"instance_id"`
	Month            string `json:"month"`
	CollectedAt      string `json:"collected_at"`
	BattleCount      int64  `json:"battle_count"`
	AkashiEncounters int64  `json:"akashi_encounters"`
	AkashiAP         int64  `json:"akashi_ap"`
}

// cl1SnapshotJSON 是响应中的快照对象，字段顺序与计划示例一致。
type cl1SnapshotJSON struct {
	DeviceID          string  `json:"device_id"`
	InstanceID        string  `json:"instance_id"`
	Month             string  `json:"month"`
	CollectedAt       string  `json:"collected_at"`
	BattleCount       int64   `json:"battle_count"`
	BattleRounds      int64   `json:"battle_rounds"`
	SortieCost        int64   `json:"sortie_cost"`
	AkashiEncounters  int64   `json:"akashi_encounters"`
	AkashiAP          int64   `json:"akashi_ap"`
	AkashiProbability float64 `json:"akashi_probability"`
	AverageStamina    float64 `json:"average_stamina"`
	NetStaminaGain    int64   `json:"net_stamina_gain"`
}

// cl1Response 是 CL1 的成功响应结构。
type cl1Response struct {
	SchemaVersion int             `json:"schema_version"`
	RequestID     string          `json:"request_id"`
	Accepted      bool            `json:"accepted"`
	Duplicate     bool            `json:"duplicate"`
	ReceivedAt    string          `json:"received_at"`
	Snapshot      cl1SnapshotJSON `json:"snapshot"`
}

// roundFloat 以十进制方式把 v 舍入到 places 位小数。
// Go 的 FormatFloat 对二进制浮点做正确舍入（恰好一半时进到偶数），
// 与计划公式引用的 Python round() 语义一致。
func roundFloat(v float64, places int) float64 {
	s := strconv.FormatFloat(v, 'f', places, 64)
	out, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return v
	}
	return out
}

// computeMetrics 按计划公式计算衍生指标，除数为零时结果为 0。
func computeMetrics(battleCount, akashiEncounters, akashiAP int64) (battleRounds, sortieCost, netGain int64, probability, averageStamina float64) {
	battleRounds = battleCount / 2
	sortieCost = battleRounds * 120
	netGain = akashiAP
	if battleRounds > 0 {
		probability = roundFloat(float64(akashiEncounters)/float64(battleRounds), 4)
	}
	if akashiEncounters > 0 {
		averageStamina = roundFloat(float64(akashiAP)/float64(akashiEncounters), 2)
	}
	return
}

// handlePostCl1 处理 POST /api/v2/post/cl1。
func (s *Server) handlePostCl1(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, r, errMethodNotAllowed(http.MethodPost))
		return
	}

	// 1. Content-Type 必须为 application/json（可带 charset）。
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, r, errUnsupportedMediaType())
		return
	}

	// 2. 读取正文并限制大小（64 KiB 上限，超限返回 413）。
	body, err := io.ReadAll(io.LimitReader(r.Body, cl1MaxBodyBytes+1))
	if err != nil {
		writeError(w, r, errInternal())
		return
	}
	if len(body) > cl1MaxBodyBytes {
		writeError(w, r, errPayloadTooLarge())
		return
	}

	// 3. 两阶段严格解析：
	//    第一阶段解成 key→RawMessage，语法错误 → INVALID_JSON，
	//    未知字段 → VALIDATION_ERROR；
	//    第二阶段按结构体解析，类型错误（含布尔冒充整数）→ VALIDATION_ERROR。
	var rawFields map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&rawFields); err != nil {
		writeError(w, r, errInvalidJSON())
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, r, errInvalidJSON())
		return
	}
	for key := range rawFields {
		if !cl1AllowedFields[key] {
			writeError(w, r, errValidation(fmt.Sprintf("未知字段 %s", key), key))
			return
		}
	}

	var req cl1Request
	if err := json.Unmarshal(body, &req); err != nil {
		var unmarshalErr *json.UnmarshalTypeError
		if errors.As(err, &unmarshalErr) {
			writeError(w, r, errValidation(
				fmt.Sprintf("字段 %s 的类型不合法", unmarshalErr.Field), unmarshalErr.Field))
			return
		}
		writeError(w, r, errInvalidJSON())
		return
	}

	// 4. 语义校验。
	if !uuidPattern.MatchString(req.RequestID) {
		writeError(w, r, errValidation("request_id 必须是合法的 UUID", "request_id"))
		return
	}
	if !deviceIDPattern.MatchString(req.DeviceID) {
		writeError(w, r, apiError{400, "INVALID_DEVICE_ID", "device_id 必须是 32 位十六进制", map[string]any{"field": "device_id"}})
		return
	}
	deviceID := r.Context().Value(deviceIDKey).(string)
	if !stringsEqualFold(req.DeviceID, deviceID) {
		writeError(w, r, errDeviceIDMismatch())
		return
	}
	if versionKey := r.Header.Get("Cloudflare-Workers-Version-Key"); versionKey != "" && !stringsEqualFold(versionKey, req.DeviceID) {
		writeError(w, r, errDeviceIDMismatch())
		return
	}
	if legacyID := r.Header.Get("X-Device-Id"); legacyID != "" && !stringsEqualFold(legacyID, req.DeviceID) {
		writeError(w, r, errDeviceIDMismatch())
		return
	}
	if !instanceIDPattern.MatchString(req.InstanceID) {
		writeError(w, r, errValidation("instance_id 必须是 16 位十六进制", "instance_id"))
		return
	}
	if !monthPattern.MatchString(req.Month) {
		writeError(w, r, errValidation("month 必须是有效的 YYYY-MM", "month"))
		return
	}
	collectedAt, err := time.Parse(time.RFC3339Nano, req.CollectedAt)
	if err != nil {
		writeError(w, r, errValidation("collected_at 必须是带时区的 RFC 3339 时间", "collected_at"))
		return
	}
	sum := sha256.Sum256(body)
	s.saveCl1(w, r, req, collectedAt, hex.EncodeToString(sum[:]), false)
}

func (s *Server) saveCl1(w http.ResponseWriter, r *http.Request, req cl1Request, collectedAt time.Time, bodyHash string, legacy bool) {
	req.DeviceID, req.InstanceID = strings.ToLower(req.DeviceID), strings.ToLower(req.InstanceID)
	receivedAt := s.now().UTC()
	// Both protocols share server-side metrics, durable dedup and snapshots.
	var battleRounds, sortieCost, netGain int64
	var probability, averageStamina float64
	if store.Cl1TotalsValid(store.Cl1Record{Month: req.Month, CollectedAt: collectedAt, BattleCount: req.BattleCount, AkashiEncounters: req.AkashiEncounters, AkashiAP: req.AkashiAP}, receivedAt) {
		battleRounds, sortieCost, netGain, probability, averageStamina = computeMetrics(req.BattleCount, req.AkashiEncounters, req.AkashiAP)
	}
	resp := cl1Response{
		SchemaVersion: schemaVersion,
		RequestID:     req.RequestID,
		Accepted:      true,
		Duplicate:     false,
		ReceivedAt:    s.now().UTC().Format(time.RFC3339),
		Snapshot: cl1SnapshotJSON{
			DeviceID:          req.DeviceID,
			InstanceID:        req.InstanceID,
			Month:             req.Month,
			CollectedAt:       req.CollectedAt,
			BattleCount:       req.BattleCount,
			BattleRounds:      battleRounds,
			SortieCost:        sortieCost,
			AkashiEncounters:  req.AkashiEncounters,
			AkashiAP:          req.AkashiAP,
			AkashiProbability: probability,
			AverageStamina:    averageStamina,
			NetStaminaGain:    netGain,
		},
	}
	responseJSON, err := marshalNoEscape(resp)
	if err != nil {
		s.logger.Error("序列化 CL1 响应失败", "err", err)
		writeError(w, r, errInternal())
		return
	}

	// 6. 事务写入：快照覆盖 + 去重登记。
	outcome, payload, err := s.store.SubmitCl1(r.Context(), store.Cl1Record{
		Legacy:            legacy,
		RequestID:         req.RequestID,
		DeviceID:          req.DeviceID,
		InstanceID:        req.InstanceID,
		Month:             req.Month,
		CollectedAt:       collectedAt,
		ReceivedAt:        receivedAt,
		BattleCount:       req.BattleCount,
		BattleRounds:      battleRounds,
		SortieCost:        sortieCost,
		AkashiEncounters:  req.AkashiEncounters,
		AkashiAP:          req.AkashiAP,
		AkashiProbability: probability,
		AverageStamina:    averageStamina,
		NetStaminaGain:    netGain,
	}, bodyHash, responseJSON)
	if err != nil {
		s.logger.Error("CL1 写入失败", "err", err)
		writeError(w, r, errInternal())
		return
	}

	switch outcome {
	case store.Cl1Accepted, store.Cl1Observed, store.Cl1Discarded:
		// 快照与去重记录已原子提交，返回刚组装的业务结果。
		if r.URL.Path == "/api/telemetry" {
			s.writeLegacyCl1(w, r, resp)
		} else {
			writeJSON(w, r, http.StatusOK, json.RawMessage(responseJSON))
		}
	case store.Cl1Duplicate:
		// 原业务结果原样返回，仅把 duplicate 标记为 true。
		var replay cl1Response
		if err := json.Unmarshal(payload, &replay); err != nil {
			s.logger.Error("回放去重响应失败", "err", err)
			writeError(w, r, errInternal())
			return
		}
		replay.Duplicate = true
		if r.URL.Path == "/api/telemetry" {
			s.writeLegacyCl1(w, r, replay)
		} else {
			writeJSON(w, r, http.StatusOK, replay)
		}
	case store.Cl1RequestConflict:
		writeError(w, r, errRequestConflict("同一 request_id 携带了不同的正文"))
	case store.Cl1StaleSnapshot:
		writeError(w, r, errStaleSnapshot())
	default:
		writeError(w, r, errInternal())
	}
}

// requireDeviceID 校验 v2 动态接口必备的设备 ID 请求头，
// 并把规范化（小写）后的值注入 context。
func requireDeviceID(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-AzurPilot-ID")
		if !deviceIDPattern.MatchString(id) {
			writeError(w, r, errInvalidDeviceID())
			return
		}
		ctx := context.WithValue(r.Context(), deviceIDKey, id)
		next(w, r.WithContext(ctx))
	}
}

type deviceIDContextKey struct{}

var deviceIDKey deviceIDContextKey

func stringsEqualFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// marshalNoEscape 序列化 JSON，不转义 HTML 字符并附带换行。
func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
