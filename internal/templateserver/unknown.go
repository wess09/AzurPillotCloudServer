package templateserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"ap-cloud-server/internal/v3db"
)

// 未知模板聚类状态。
const (
	unknownPending  = "pending"
	unknownMapped   = "mapped"
	unknownRejected = "rejected"
	unknownMerged   = "merged"
)

const (
	// 客户端上传的模板图标上限；与主 API 的转发上限一致。
	unknownMaxBody     = 512 << 10
	unknownMaxIconByte = 384 << 10
	unknownMinSide     = 8
	unknownMaxSide     = 512
)

var (
	errClusterNotFound = errors.New("unknown cluster not found")
	errClusterRejected = errors.New("unknown cluster rejected")
	errClusterMerged   = errors.New("unknown cluster merged")
)

type unknownUploadInput struct {
	Scene       string `json:"scene"`
	ImageBase64 string `json:"image_base64"`
	SHA256      string `json:"sha256"`
	Client      string `json:"client"`
}

type unknownMapInput struct {
	Item     string `json:"item"`
	Tag      string `json:"tag"`
	Name     string `json:"name"`
	Scene    string `json:"scene"`
	Kind     string `json:"kind"`
	Register *bool  `json:"register"`
}

type unknownView struct {
	ClusterKey  string    `json:"cluster_key"`
	SHA256      string    `json:"sha256"`
	PHash       string    `json:"phash"`
	Scene       string    `json:"scene"`
	Item        string    `json:"item"`
	Tag         string    `json:"tag"`
	Status      string    `json:"status"`
	Occurrences int64     `json:"occurrences"`
	Client      string    `json:"client"`
	ImageBase64 string    `json:"image_base64"`
	FirstSeenAt time.Time `json:"first_seen_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
}

func toUnknownView(c v3db.UnknownTemplate) unknownView {
	return unknownView{
		ClusterKey: c.ClusterKey, SHA256: c.SHA256, PHash: c.PHash,
		Scene: c.Scene, Item: c.Item, Tag: c.Tag, Status: c.Status,
		Occurrences: c.Occurrences, Client: c.Client,
		ImageBase64: base64.StdEncoding.EncodeToString(c.Icon),
		FirstSeenAt: c.FirstSeenAt, LastSeenAt: c.LastSeenAt,
	}
}

// ---- 客户端上传 / 匹配 ----

func (s *Server) handleIngestUnknown(w http.ResponseWriter, r *http.Request) {
	if s.ingestTok == "" {
		writeErr(w, http.StatusServiceUnavailable, "INGEST_DISABLED", "未知模板上传未启用", nil)
		return
	}
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "缺少上传凭据", nil)
		return
	}
	got := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	if subtle.ConstantTimeCompare([]byte(got), []byte(s.ingestTok)) != 1 {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "上传凭据无效", nil)
		return
	}
	var in unknownUploadInput
	if err := decodeBody(w, r, unknownMaxBody, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "INVALID_JSON", "请求体不是合法的 JSON", nil)
		return
	}
	if in.Scene != v3db.SceneCommission && in.Scene != v3db.SceneResearch {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "scene 只能为 commission 或 research", map[string]any{"field": "scene"})
		return
	}
	raw, err := base64.StdEncoding.DecodeString(in.ImageBase64)
	if err != nil || len(raw) == 0 || len(raw) > unknownMaxIconByte {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "image_base64 非法或过大", map[string]any{"field": "image_base64"})
		return
	}
	img, err := decodeTemplatePNG(raw, unknownMinSide, unknownMaxSide)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error(), map[string]any{"field": "image_base64"})
		return
	}
	sum := sha256.Sum256(raw)
	sha := hex.EncodeToString(sum[:])
	if in.SHA256 != "" && !strings.EqualFold(strings.TrimSpace(in.SHA256), sha) {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "sha256 与图片不一致", map[string]any{"field": "sha256"})
		return
	}
	client := strings.TrimSpace(in.Client)
	if len(client) > 64 {
		client = client[:64]
	}
	hash := dHash(img)
	ctx := r.Context()

	known, err := s.knownTemplates(ctx)
	if err != nil {
		s.logger.Error("读取已知模板失败", "err", err)
		writeErr(w, http.StatusInternalServerError, "INTERNAL_ERROR", "内部错误", nil)
		return
	}
	if match, ok := matchKnownTemplate(known, hash, in.Scene); ok {
		writeJSON(w, http.StatusOK, map[string]any{
			"schema_version": 3,
			"status":         "matched",
			"template_key":   match.Key,
			"item":           match.Item,
			"tag":            match.Tag,
			"revision":       s.currentRevision(ctx),
		})
		return
	}

	cluster, created, err := s.matchOrCreateCluster(ctx, hash, raw, sha, in.Scene, client)
	if err != nil {
		s.logger.Error("未知模板登记失败", "err", err)
		writeErr(w, http.StatusInternalServerError, "INTERNAL_ERROR", "内部错误", nil)
		return
	}
	status := cluster.Status
	if created {
		status = "registered"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"schema_version": 3,
		"status":         status,
		"cluster_key":    cluster.ClusterKey,
		"item":           cluster.Item,
		"tag":            cluster.Tag,
		"occurrences":    cluster.Occurrences,
		"revision":       s.currentRevision(ctx),
	})
}

type knownTemplate struct {
	Key   string
	Item  string
	Tag   string
	Scene string
	Hash  uint64
}

// knownTemplates 缓存启用模板的感知哈希；模板变更时显式失效。
func (s *Server) knownTemplates(ctx context.Context) ([]knownTemplate, error) {
	s.knownMu.RLock()
	if s.knownLoaded {
		out := s.known
		s.knownMu.RUnlock()
		return out, nil
	}
	s.knownMu.RUnlock()

	var rows []v3db.Template
	if err := s.db.WithContext(ctx).Select("key", "item", "tag", "scene_context", "icon").
		Where("enabled = ?", true).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]knownTemplate, 0, len(rows))
	for _, row := range rows {
		img, err := png.Decode(bytes.NewReader(row.Icon))
		if err != nil {
			continue
		}
		out = append(out, knownTemplate{
			Key: row.Key, Item: row.Item, Tag: row.Tag,
			Scene: row.SceneContext, Hash: dHash(img),
		})
	}
	s.knownMu.Lock()
	s.known = out
	s.knownLoaded = true
	s.knownMu.Unlock()
	return out, nil
}

func (s *Server) invalidateKnown() {
	s.knownMu.Lock()
	s.known = nil
	s.knownLoaded = false
	s.knownMu.Unlock()
}

func matchKnownTemplate(entries []knownTemplate, hash uint64, scene string) (knownTemplate, bool) {
	var match knownTemplate
	best, second := 65, 65
	for _, e := range entries {
		if e.Scene != "all" && e.Scene != scene {
			continue
		}
		d := hamming64(hash, e.Hash)
		if d < best {
			second = best
			best = d
			match = e
			continue
		}
		if d < second {
			second = d
		}
	}
	if match.Key == "" || best > phashMatchThreshold {
		return knownTemplate{}, false
	}
	if second != 65 && second-best < phashMargin {
		return knownTemplate{}, false
	}
	return match, true
}

// matchOrCreateCluster 先按感知哈希匹配既有聚类，匹配不到则登记新聚类。
// created 表示本次是否新建；即使并发下被别的请求抢先创建，也只做计数递增。
func (s *Server) matchOrCreateCluster(ctx context.Context, hash uint64, raw []byte, sha, scene, client string) (v3db.UnknownTemplate, bool, error) {
	now := s.now().UTC()
	var clusters []v3db.UnknownTemplate
	if err := s.db.WithContext(ctx).Select("cluster_key", "scene", "phash", "sha256", "occurrences", "status", "item", "tag").
		Where("status <> ?", unknownRejected).Find(&clusters).Error; err != nil {
		return v3db.UnknownTemplate{}, false, err
	}
	var candidate *v3db.UnknownTemplate
	best, second := 65, 65
	for i := range clusters {
		ph, err := parsePHash(clusters[i].PHash)
		if err != nil {
			continue
		}
		d := hamming64(hash, ph)
		if d < best {
			second = best
			best = d
			candidate = &clusters[i]
			continue
		}
		if d < second {
			second = d
		}
	}
	if candidate != nil && best <= phashMatchThreshold && (second == 65 || second-best >= phashMargin) {
		key := candidate.ClusterKey
		if candidate.Status == unknownMerged {
			resolved, err := s.resolveAlias(ctx, key)
			if err != nil {
				return v3db.UnknownTemplate{}, false, err
			}
			key = resolved
		}
		row, err := s.touchCluster(ctx, key, now, client)
		return row, false, err
	}

	key := "U-" + formatPHash(hash)
	row := v3db.UnknownTemplate{
		ClusterKey: key, Scene: scene, PHash: formatPHash(hash), SHA256: sha,
		Icon: raw, Occurrences: 1, Status: unknownPending, Source: "client",
		Client: client, FirstSeenAt: now, LastSeenAt: now,
	}
	res := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if res.Error != nil {
		return v3db.UnknownTemplate{}, false, res.Error
	}
	if res.RowsAffected > 0 {
		return row, true, nil
	}
	// 并发或此前被拒绝：重新读取并按现状处理。
	var existing v3db.UnknownTemplate
	if err := s.db.WithContext(ctx).Where("cluster_key = ?", key).Take(&existing).Error; err != nil {
		return v3db.UnknownTemplate{}, false, err
	}
	if existing.Status == unknownRejected {
		return existing, false, nil
	}
	if existing.Status == unknownMerged {
		resolved, err := s.resolveAlias(ctx, key)
		if err != nil {
			return v3db.UnknownTemplate{}, false, err
		}
		existing, err = s.touchCluster(ctx, resolved, now, client)
		return existing, false, err
	}
	existing, err := s.touchCluster(ctx, key, now, client)
	return existing, false, err
}

func (s *Server) touchCluster(ctx context.Context, key string, now time.Time, client string) (v3db.UnknownTemplate, error) {
	var out v3db.UnknownTemplate
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row v3db.UnknownTemplate
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("cluster_key = ?", key).Take(&row).Error; err != nil {
			return err
		}
		row.Occurrences++
		row.LastSeenAt = now
		if row.Client == "" && client != "" {
			row.Client = client
		}
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		out = row
		return nil
	})
	return out, err
}

func (s *Server) resolveAlias(ctx context.Context, key string) (string, error) {
	for i := 0; i < 8; i++ {
		var alias v3db.UnknownClusterAlias
		err := s.db.WithContext(ctx).Where("from_key = ?", key).Take(&alias).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return key, nil
		}
		if err != nil {
			return "", err
		}
		if alias.ToKey == "" || alias.ToKey == key {
			return key, nil
		}
		key = alias.ToKey
	}
	return key, fmt.Errorf("alias chain too deep")
}

func (s *Server) currentRevision(ctx context.Context) uint64 {
	var state v3db.TemplateState
	if err := s.db.WithContext(ctx).Select("current_revision").Where("id = ?", 1).Take(&state).Error; err != nil {
		return 0
	}
	return state.CurrentRevision
}

// ---- 管理：列表 / 映射 / 合并 / 拒绝 ----

func (s *Server) handleListUnknown(w http.ResponseWriter, r *http.Request) {
	q := s.db.WithContext(r.Context()).Model(&v3db.UnknownTemplate{})
	values := r.URL.Query()
	if status := strings.TrimSpace(values.Get("status")); status != "" {
		q = q.Where("status = ?", status)
	}
	if scene := strings.TrimSpace(values.Get("scene")); scene != "" {
		q = q.Where("scene = ?", scene)
	}
	limit := 200
	if v := values.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	var rows []v3db.UnknownTemplate
	if err := q.Order("last_seen_at DESC").Limit(limit).Find(&rows).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL_ERROR", "内部错误", nil)
		return
	}
	out := make([]unknownView, 0, len(rows))
	for _, row := range rows {
		out = append(out, toUnknownView(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema_version": 3, "clusters": out})
}

func (s *Server) handleMapUnknown(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !keyRE.MatchString(key) {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "无效的 cluster_key", nil)
		return
	}
	var in unknownMapInput
	if err := decodeBody(w, r, 8<<10, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "INVALID_JSON", "请求体不是合法的 JSON", nil)
		return
	}
	in.Item = strings.TrimSpace(in.Item)
	in.Tag = strings.TrimSpace(in.Tag)
	in.Name = strings.TrimSpace(in.Name)
	if in.Item == "" || len(in.Item) > 128 {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "item 不能为空且不超过 128 字符", map[string]any{"field": "item"})
		return
	}
	if len(in.Tag) > 32 {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "tag 不超过 32 字符", map[string]any{"field": "tag"})
		return
	}
	if len(in.Name) > 128 {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "name 不超过 128 字符", map[string]any{"field": "name"})
		return
	}
	if in.Scene != "" && in.Scene != "all" && in.Scene != v3db.SceneCommission && in.Scene != v3db.SceneResearch {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "scene 必须为 all、commission 或 research", map[string]any{"field": "scene"})
		return
	}
	if in.Kind != "" && in.Kind != v3db.KindFormal && in.Kind != v3db.KindTemporary {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "kind 必须为 formal 或 temporary", map[string]any{"field": "kind"})
		return
	}
	register := in.Register == nil || *in.Register

	now := s.now().UTC()
	var revision uint64
	err := s.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		var cluster v3db.UnknownTemplate
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("cluster_key = ?", key).Take(&cluster).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errClusterNotFound
			}
			return err
		}
		switch cluster.Status {
		case unknownRejected:
			return errClusterRejected
		case unknownMerged:
			return errClusterMerged
		}
		var state v3db.TemplateState
		if err := tx.Where("id = ?", 1).Take(&state).Error; err != nil {
			return err
		}
		revision = state.CurrentRevision + 1
		if err := tx.Create(&v3db.UnknownMapping{
			Revision: revision, ClusterKey: key, Item: in.Item, Tag: in.Tag,
		}).Error; err != nil {
			return err
		}
		cluster.Item = in.Item
		cluster.Tag = in.Tag
		cluster.Status = unknownMapped
		cluster.LastSeenAt = now
		if err := tx.Save(&cluster).Error; err != nil {
			return err
		}
		if !register {
			return nil
		}
		scene := in.Scene
		if scene == "" {
			scene = cluster.Scene
		}
		if scene == "" {
			scene = "all"
		}
		name := in.Name
		if name == "" {
			name = in.Item
		}
		kind := in.Kind
		if kind == "" {
			kind = v3db.KindTemporary
		}
		tpl := v3db.Template{
			Key: key, Name: name, Item: in.Item, Tag: in.Tag,
			SceneContext: scene, Kind: kind,
			SHA256: cluster.SHA256, IconSHA256: cluster.SHA256, Icon: cluster.Icon,
			Enabled: true, CreatedAt: now, UpdatedAt: now,
		}
		return tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "key"}},
			DoUpdates: clause.Assignments(map[string]any{
				"name": name, "item": in.Item, "tag": in.Tag,
				"scene_context": scene, "kind": kind,
				"sha256": cluster.SHA256, "icon_sha256": cluster.SHA256,
				"icon": cluster.Icon, "enabled": true, "updated_at": now,
			}),
		}).Create(&tpl).Error
	})
	if err != nil {
		switch {
		case errors.Is(err, errClusterNotFound):
			writeErr(w, http.StatusNotFound, "NOT_FOUND", "聚类不存在", nil)
		case errors.Is(err, errClusterRejected):
			writeErr(w, http.StatusConflict, "CLUSTER_REJECTED", "聚类已被拒绝", nil)
		case errors.Is(err, errClusterMerged):
			writeErr(w, http.StatusConflict, "CLUSTER_MERGED", "聚类已合并，请使用目标 cluster_key", nil)
		default:
			s.logger.Error("未知模板映射失败", "err", err)
			writeErr(w, http.StatusInternalServerError, "INTERNAL_ERROR", "内部错误", nil)
		}
		return
	}
	s.invalidateKnown()
	writeJSON(w, http.StatusOK, map[string]any{
		"schema_version": 3,
		"cluster_key":    key,
		"status":         unknownMapped,
		"revision":       revision,
		"registered":     register,
		"publish_needed": true,
	})
}

func (s *Server) handleRejectUnknown(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !keyRE.MatchString(key) {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "无效的 cluster_key", nil)
		return
	}
	now := s.now().UTC()
	err := s.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		var cluster v3db.UnknownTemplate
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("cluster_key = ?", key).Take(&cluster).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errClusterNotFound
			}
			return err
		}
		cluster.Status = unknownRejected
		cluster.LastSeenAt = now
		if err := tx.Save(&cluster).Error; err != nil {
			return err
		}
		return tx.Model(&v3db.Template{}).Where("key = ?", key).
			Updates(map[string]any{"enabled": false, "updated_at": now}).Error
	})
	if err != nil {
		if errors.Is(err, errClusterNotFound) {
			writeErr(w, http.StatusNotFound, "NOT_FOUND", "聚类不存在", nil)
			return
		}
		s.logger.Error("拒绝未知模板失败", "err", err)
		writeErr(w, http.StatusInternalServerError, "INTERNAL_ERROR", "内部错误", nil)
		return
	}
	s.invalidateKnown()
	writeJSON(w, http.StatusOK, map[string]any{"schema_version": 3, "cluster_key": key, "status": unknownRejected})
}

func (s *Server) handleMergeUnknown(w http.ResponseWriter, r *http.Request) {
	var in struct {
		FromKey string `json:"from_key"`
		ToKey   string `json:"to_key"`
	}
	if err := decodeBody(w, r, 4<<10, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "INVALID_JSON", "请求体不是合法的 JSON", nil)
		return
	}
	in.FromKey = strings.TrimSpace(in.FromKey)
	in.ToKey = strings.TrimSpace(in.ToKey)
	if !keyRE.MatchString(in.FromKey) || !keyRE.MatchString(in.ToKey) {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "无效的 cluster_key", nil)
		return
	}
	if in.FromKey == in.ToKey {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "from_key 与 to_key 不能相同", nil)
		return
	}
	now := s.now().UTC()
	var revision uint64
	err := s.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		keys := []string{in.FromKey, in.ToKey}
		if keys[0] > keys[1] {
			keys[0], keys[1] = keys[1], keys[0]
		}
		var rows []v3db.UnknownTemplate
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("cluster_key IN ?", keys).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) != 2 {
			return errClusterNotFound
		}
		var from, to *v3db.UnknownTemplate
		for i := range rows {
			if rows[i].ClusterKey == in.FromKey {
				from = &rows[i]
			} else {
				to = &rows[i]
			}
		}
		if from.Status == unknownRejected || to.Status == unknownRejected {
			return errClusterRejected
		}
		if from.Status == unknownMerged || to.Status == unknownMerged {
			return errClusterMerged
		}
		to.Occurrences += from.Occurrences
		from.Status = unknownMerged
		from.LastSeenAt = now
		if err := tx.Save(to).Error; err != nil {
			return err
		}
		if err := tx.Save(from).Error; err != nil {
			return err
		}
		var state v3db.TemplateState
		if err := tx.Where("id = ?", 1).Take(&state).Error; err != nil {
			return err
		}
		revision = state.CurrentRevision + 1
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "from_key"}},
			DoUpdates: clause.AssignmentColumns([]string{"to_key", "revision"}),
		}).Create(&v3db.UnknownClusterAlias{
			FromKey: in.FromKey, ToKey: in.ToKey, Revision: revision,
		}).Error; err != nil {
			return err
		}
		// 源聚类已有映射而目标没有时，把映射迁移到目标，避免信息丢失。
		var toMappings int64
		if err := tx.Model(&v3db.UnknownMapping{}).Where("cluster_key = ?", in.ToKey).Count(&toMappings).Error; err != nil {
			return err
		}
		if toMappings == 0 {
			var fromMapping v3db.UnknownMapping
			err := tx.Where("cluster_key = ?", in.FromKey).Order("revision DESC, id DESC").Take(&fromMapping).Error
			if err == nil {
				if err := tx.Create(&v3db.UnknownMapping{
					Revision: revision, ClusterKey: in.ToKey,
					Item: fromMapping.Item, Tag: fromMapping.Tag,
				}).Error; err != nil {
					return err
				}
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		}
		return tx.Model(&v3db.Template{}).Where("key = ?", in.FromKey).
			Updates(map[string]any{"enabled": false, "updated_at": now}).Error
	})
	if err != nil {
		switch {
		case errors.Is(err, errClusterNotFound):
			writeErr(w, http.StatusNotFound, "NOT_FOUND", "聚类不存在", nil)
		case errors.Is(err, errClusterRejected):
			writeErr(w, http.StatusConflict, "CLUSTER_REJECTED", "聚类已被拒绝", nil)
		case errors.Is(err, errClusterMerged):
			writeErr(w, http.StatusConflict, "CLUSTER_MERGED", "聚类已合并", nil)
		default:
			s.logger.Error("合并未知模板失败", "err", err)
			writeErr(w, http.StatusInternalServerError, "INTERNAL_ERROR", "内部错误", nil)
		}
		return
	}
	s.invalidateKnown()
	writeJSON(w, http.StatusOK, map[string]any{
		"schema_version": 3,
		"from_key":       in.FromKey,
		"to_key":         in.ToKey,
		"revision":       revision,
	})
}

// decodeTemplatePNG 先校验尺寸再解码，避免解压炸弹；只接受正方形 PNG。
func decodeTemplatePNG(raw []byte, minSide, maxSide int) (image.Image, error) {
	cfg, err := png.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("模板必须是 PNG")
	}
	if cfg.Width != cfg.Height || cfg.Width < minSide || cfg.Width > maxSide {
		return nil, fmt.Errorf("模板必须是 %d–%d 像素的正方形 PNG", minSide, maxSide)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("模板必须是可解码的 PNG")
	}
	return img, nil
}
