package httpapi

import (
	"bytes"
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

// AP's server checker sends composite IDs, including cn_ios and cn_channel.
// Reuse the gateway/cache and response fields of v2 without requiring a new
// device header from historical clients.
func (s *Server) handleLegacyServer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, r, errMethodNotAllowed("GET"))
		return
	}
	id := r.PathValue("id")
	split := strings.LastIndexByte(id, '_')
	if split <= 0 {
		writeError(w, r, errValidation("需要 region_server_id", "id"))
		return
	}
	serverID, err := strconv.Atoi(id[split+1:])
	if err != nil || serverID <= 0 {
		writeError(w, r, errValidation("server_id 必须为正整数", "id"))
		return
	}
	copy := r.Clone(r.Context())
	query := copy.URL.Query()
	query.Set("region", id[:split])
	query.Set("server_id", strconv.Itoa(serverID))
	copy.URL.RawQuery = query.Encode()
	s.handleGetServer(w, copy)
}

// The old AP submitter omits request_id/collected_at and reports raw Akashi AP
// under net_stamina_gain. Never trust its separately computed derived metrics.
func (s *Server) handleLegacyTelemetry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, r, errMethodNotAllowed("POST"))
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		writeError(w, r, errUnsupportedMediaType())
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, cl1MaxBodyBytes+1))
	if err != nil {
		writeError(w, r, errInvalidJSON())
		return
	}
	if len(body) > cl1MaxBodyBytes {
		writeError(w, r, errPayloadTooLarge())
		return
	}
	var old struct {
		DeviceID         string          `json:"device_id"`
		InstanceID       string          `json:"instance_id"`
		Month            string          `json:"month"`
		BattleCount      int64           `json:"battle_count"`
		AkashiEncounters int64           `json:"akashi_encounters"`
		NetStaminaGain   *int64          `json:"net_stamina_gain"`
		AkashiAP         *int64          `json:"akashi_ap"`
		RequestID        string          `json:"request_id"`
		CollectedAt      string          `json:"collected_at"`
		BattleRounds     json.RawMessage `json:"battle_rounds"`
		SortieCost       json.RawMessage `json:"sortie_cost"`
		Probability      json.RawMessage `json:"akashi_probability"`
		Average          json.RawMessage `json:"average_stamina"`
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if err = d.Decode(&old); err != nil {
		writeError(w, r, errValidation("无效旧版遥测正文", "body"))
		return
	}
	if err = d.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, r, errInvalidJSON())
		return
	}
	old.DeviceID = strings.ToLower(old.DeviceID)
	old.InstanceID = strings.ToLower(old.InstanceID)
	originalID := old.DeviceID
	if !regexp.MustCompile(`^[0-9a-f]{32,64}$`).MatchString(old.DeviceID) {
		writeError(w, r, errValidation("无效设备标识", "device_id"))
		return
	}
	if len(old.DeviceID) != 32 {
		d := sha256.Sum256([]byte("legacy-device\x00" + old.DeviceID))
		old.DeviceID = hex.EncodeToString(d[:16])
	}
	if !instanceIDPattern.MatchString(old.InstanceID) || !monthPattern.MatchString(old.Month) {
		writeError(w, r, errValidation("无效设备、实例或月份", "body"))
		return
	}
	for _, key := range []string{"X-AzurPilot-ID", "X-Device-Id", "Cloudflare-Workers-Version-Key"} {
		if id := r.Header.Get(key); id != "" && !strings.EqualFold(id, old.DeviceID) && !strings.EqualFold(id, originalID) {
			writeError(w, r, errDeviceIDMismatch())
			return
		}
	}
	ap := old.AkashiAP
	if ap == nil {
		ap = old.NetStaminaGain
	}
	if ap == nil || *ap < 0 || old.BattleCount < 1 || old.BattleCount > 1e12 || old.AkashiEncounters < 0 || old.AkashiEncounters > 1e12 || *ap > 1e15 {
		writeError(w, r, errValidation("无效计数或行动力", "body"))
		return
	}
	if old.AkashiAP != nil && old.NetStaminaGain != nil && *old.AkashiAP != *old.NetStaminaGain {
		writeError(w, r, errValidation("行动力字段冲突", "akashi_ap"))
		return
	}
	canonical, _ := json.Marshal(struct {
		Device, Instance, Month string
		Battles, Encounters, AP int64
		CollectedAt             string
	}{old.DeviceID, old.InstanceID, old.Month, old.BattleCount, old.AkashiEncounters, *ap, old.CollectedAt})
	digest := sha256.Sum256(append([]byte("legacy-telemetry\x00"), canonical...))
	id := old.RequestID
	if id == "" {
		id = fmt.Sprintf("%x-%x-%x-%x-%x", digest[:4], digest[4:6], digest[6:8], digest[8:10], digest[10:16])
	}
	if !uuidPattern.MatchString(id) {
		writeError(w, r, errValidation("request_id 必须是 UUID", "request_id"))
		return
	}
	at := s.now().UTC()
	if old.CollectedAt != "" {
		at, err = time.Parse(time.RFC3339Nano, old.CollectedAt)
		if err != nil {
			writeError(w, r, errValidation("collected_at 需要带时区", "collected_at"))
			return
		}
	}
	req := cl1Request{RequestID: id, DeviceID: old.DeviceID, InstanceID: old.InstanceID, Month: old.Month, CollectedAt: at.Format(time.RFC3339Nano), BattleCount: old.BattleCount, AkashiEncounters: old.AkashiEncounters, AkashiAP: *ap}
	s.saveCl1(w, r, req, at, hex.EncodeToString(digest[:]), old.CollectedAt == "")
}

func legacyPublic(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Idempotency-Key, X-AzurPilot-ID, X-Device-Id")
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		next(w, r)
	}
}

func (s *Server) writeLegacyCl1(w http.ResponseWriter, r *http.Request, response cl1Response) {
	writeJSON(w, r, 200, struct {
		cl1Response
		Status   string `json:"status"`
		Message  string `json:"message"`
		Device   string `json:"device_id"`
		Instance string `json:"instance_id"`
	}{response, "success", "遥测数据已保存", response.Snapshot.DeviceID, response.Snapshot.InstanceID})
}

func (s *Server) handleLegacyAnnouncement(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeError(w, r, errMethodNotAllowed("GET"))
		return
	}
	a, err := s.store.LatestAnnouncement(r.Context())
	if err != nil {
		writeError(w, r, errServiceUnavailable())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if a == nil || r.URL.Query().Get("id") == a.ID {
		writeJSON(w, r, 200, map[string]any{})
		return
	}
	content, url := "", ""
	if a.Content != nil {
		content = *a.Content
	}
	if a.URL != nil {
		url = *a.URL
	}
	writeJSON(w, r, 200, map[string]any{"announcementId": a.ID, "title": a.Title, "content": content, "url": url})
}

func (s *Server) handleLegacyForceUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeError(w, r, errMethodNotAllowed("GET"))
		return
	}
	force, err := s.store.GetUpdatePolicy(r.Context())
	if err != nil {
		writeError(w, r, errServiceUnavailable())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, r, 200, force)
}

func (s *Server) handleLegacyMaintenance(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeError(w, r, errMethodNotAllowed("GET"))
		return
	}
	servers := map[string]any{}
	for server, hours := range map[string]int{"cn": 8, "en": -7, "jp": 9, "tw": 8} {
		m, err := s.store.LatestMaintenance(r.Context(), server)
		if err != nil {
			writeError(w, r, errServiceUnavailable())
			return
		}
		if m == nil {
			continue
		}
		servers[server] = legacyMaintenance(m, hours)
	}
	data := map[string]any{"servers": servers}
	if cn, ok := servers["cn"].(map[string]any); ok {
		for k, v := range cn {
			data[k] = v
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, r, 200, map[string]any{"status": "success", "data": data})
}

func legacyMaintenance(m *store.Maintenance, hours int) map[string]any {
	at := m.StartsAt.In(time.FixedZone("server", hours*3600))
	return map[string]any{"maintenance_date": at.Format("2006-01-02"), "start_time": at.Format("15:04"), "timezone": fmt.Sprintf("UTC%+d", hours), "name": m.Name}
}
