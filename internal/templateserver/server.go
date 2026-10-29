// Package templateserver 是独立部署的模板管理与发布服务。
//
// 它拥有自己的数据库，负责模板 CRUD、Ed25519 签名、增量生成，
// 并把 JSON 与 PNG 发布到 Cloudflare Worker（绑定自定义域名）。
package templateserver

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"ap-cloud-server/internal/v3db"
)

var (
	keyRE  = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
	iconRE = regexp.MustCompile(`^[0-9a-f]{64}\.png$`)
)

type Server struct {
	cfg       Config
	db        *gorm.DB
	signer    *Signer
	publisher *Publisher
	logger    *slog.Logger
	http      *http.Server
	now       func() time.Time
	ingestTok string

	knownMu     sync.RWMutex
	known       []knownTemplate
	knownLoaded bool
}

func NewServer(cfg Config, db *gorm.DB, signer *Signer, logger *slog.Logger) (*Server, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if signer == nil {
		return nil, fmt.Errorf("signer is required")
	}
	if _, err := v3db.EnsureState(context.Background(), db, cfg.PublicBase); err != nil {
		return nil, fmt.Errorf("init template state: %w", err)
	}
	s := &Server{
		cfg:       cfg,
		db:        db,
		signer:    signer,
		logger:    logger,
		now:       func() time.Time { return time.Now().UTC() },
		ingestTok: cfg.IngestToken,
	}
	s.publisher = &Publisher{
		Dir: cfg.PublishDir, PublicBase: cfg.PublicBase,
		WorkerName: cfg.WorkerName, WranglerImage: cfg.WranglerImage,
		CFDeploy: cfg.CFDeploy, AccountID: cfg.CFAccountID, APIToken: cfg.CFAPIToken,
		SelfCheck: cfg.SelfCheck, CheckAttempts: cfg.CheckAttempts, CheckDelay: cfg.CheckDelay,
		Logger: logger,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /api/v3/get/templates/manifest", s.handleManifest)
	mux.HandleFunc("GET /api/v3/get/templates/delta", s.handleDelta)
	mux.HandleFunc("GET /api/v3/get/mappings", s.handleMappings)
	mux.HandleFunc("GET /icons/{name}", s.handleIcon)
	// 客户端未知模板上传（由主 API 携带 TEMPLATE_INGEST_TOKEN 转发）。
	mux.HandleFunc("POST /api/v3/post/templates/unknown", s.handleIngestUnknown)
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /admin/", s.handleWebUI)
	mux.HandleFunc("GET /api/v1/status", s.admin(s.handleStatus))
	mux.HandleFunc("GET /api/v1/templates", s.admin(s.handleListTemplates))
	mux.HandleFunc("POST /api/v1/templates", s.admin(s.handleUpsertTemplate))
	mux.HandleFunc("PATCH /api/v1/templates/{key}", s.admin(s.handleUpdateTemplate))
	mux.HandleFunc("POST /api/v1/templates/{key}/rename", s.admin(s.handleRenameTemplate))
	mux.HandleFunc("DELETE /api/v1/templates/{key}", s.admin(s.handleDeleteTemplate))
	mux.HandleFunc("POST /api/v1/templates/publish", s.admin(s.handlePublish))
	mux.HandleFunc("POST /api/v1/templates/import", s.admin(s.handleImportCatalog))
	mux.HandleFunc("GET /api/v1/templates/releases", s.admin(s.handleReleases))
	mux.HandleFunc("GET /api/v1/templates/unknown", s.admin(s.handleListUnknown))
	mux.HandleFunc("POST /api/v1/templates/unknown/{key}/map", s.admin(s.handleMapUnknown))
	mux.HandleFunc("POST /api/v1/templates/unknown/{key}/reject", s.admin(s.handleRejectUnknown))
	mux.HandleFunc("POST /api/v1/templates/unknown/merge", s.admin(s.handleMergeUnknown))
	s.http = &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      180 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	return s, nil
}

func (s *Server) Handler() http.Handler              { return s.http.Handler }
func (s *Server) ListenAndServe() error              { return s.http.ListenAndServe() }
func (s *Server) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// ---- 公开读取 ----

func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request) {
	body, err := os.ReadFile(filepath.Join(s.cfg.PublishDir, "manifest.json"))
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "TEMPLATE_NOT_PUBLISHED", "模板尚未发布", nil)
		return
	}
	sig := readTrimmed(filepath.Join(s.cfg.PublishDir, "manifest.sig"))
	sha := readTrimmed(filepath.Join(s.cfg.PublishDir, "manifest.sha256"))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=60")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if sig != "" {
		w.Header().Set("X-Manifest-Signature", sig)
	}
	if sha != "" {
		w.Header().Set("X-Manifest-SHA256", sha)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (s *Server) handleDelta(w http.ResponseWriter, r *http.Request) {
	from := strings.TrimSpace(r.URL.Query().Get("from"))
	if from == "" {
		from = "0"
	}
	if _, err := strconv.ParseUint(from, 10, 32); err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "from 必须是非负整数", map[string]any{"field": "from"})
		return
	}
	path := filepath.Join(s.cfg.PublishDir, "deltas", from+".json")
	if _, err := os.Stat(path); err != nil {
		path = filepath.Join(s.cfg.PublishDir, "deltas", "0.json")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "TEMPLATE_NOT_PUBLISHED", "模板尚未发布", nil)
		return
	}
	writePublicJSON(w, body, "public, max-age=30")
}

func (s *Server) handleMappings(w http.ResponseWriter, r *http.Request) {
	body, err := os.ReadFile(filepath.Join(s.cfg.PublishDir, "mappings.json"))
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "TEMPLATE_NOT_PUBLISHED", "模板尚未发布", nil)
		return
	}
	writePublicJSON(w, body, "public, max-age=30")
}

func (s *Server) handleIcon(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !iconRE.MatchString(name) {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "图标不存在", nil)
		return
	}
	body, err := os.ReadFile(filepath.Join(s.cfg.PublishDir, "icons", name))
	if err != nil {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "图标不存在", nil)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func writePublicJSON(w http.ResponseWriter, body []byte, cache string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", cache)
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func readTrimmed(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// ---- 管理 API ----

func (s *Server) admin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.AdminToken == "" {
			writeErr(w, http.StatusServiceUnavailable, "ADMIN_DISABLED", "管理接口未启用", nil)
			return
		}
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "缺少管理凭据", nil)
			return
		}
		got := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.AdminToken)) != 1 {
			writeErr(w, http.StatusForbidden, "FORBIDDEN", "管理凭据无效", nil)
			return
		}
		next(w, r)
	}
}

type templateInput struct {
	Key            string `json:"key"`
	Name           string `json:"name"`
	Item           string `json:"item"`
	Tag            string `json:"tag"`
	Scene          string `json:"scene"`
	Kind           string `json:"kind"`
	ImageBase64    string `json:"image_base64"`
	AllowDuplicate bool   `json:"allow_duplicate"`
}

type templateView struct {
	ID          uint64    `json:"id"`
	Key         string    `json:"key"`
	Name        string    `json:"name"`
	Item        string    `json:"item"`
	Tag         string    `json:"tag"`
	Scene       string    `json:"scene"`
	Kind        string    `json:"kind"`
	SHA256      string    `json:"sha256"`
	IconSHA256  string    `json:"icon_sha256"`
	ImageBase64 string    `json:"image_base64"`
	Enabled     bool      `json:"enabled"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func toView(t v3db.Template) templateView {
	return templateView{
		ID: t.ID, Key: t.Key, Name: t.Name, Item: t.Item, Tag: t.Tag,
		Scene: t.SceneContext, Kind: t.Kind, SHA256: t.SHA256, IconSHA256: t.IconSHA256,
		ImageBase64: base64.StdEncoding.EncodeToString(t.Icon),
		Enabled:     t.Enabled, UpdatedAt: t.UpdatedAt,
	}
}

func (s *Server) handleUpsertTemplate(w http.ResponseWriter, r *http.Request) {
	var in templateInput
	if err := decodeBody(w, r, 256<<10, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "INVALID_JSON", "请求体不是合法的 JSON", nil)
		return
	}
	raw, err := base64.StdEncoding.DecodeString(in.ImageBase64)
	if err != nil || len(raw) == 0 {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "image_base64 非法", map[string]any{"field": "image_base64"})
		return
	}
	if _, err := decodeTemplatePNG(raw, 49, 49); err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error(), map[string]any{"field": "image_base64"})
		return
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	if strings.TrimSpace(in.Key) == "" {
		in.Key = "T-" + hash[:16]
	}
	if err := validateTemplateInput(in); err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	if !in.AllowDuplicate {
		var dup v3db.Template
		err := s.db.WithContext(r.Context()).Select("key").
			Where("sha256 = ? AND key <> ? AND enabled = ?", hash, in.Key, true).
			Order("id ASC").Take(&dup).Error
		if err == nil {
			writeErr(w, http.StatusConflict, "TEMPLATE_DUPLICATE", "已存在相同图标，如需强制新建请设置 allow_duplicate",
				map[string]any{"existing_key": dup.Key})
			return
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			writeErr(w, http.StatusInternalServerError, "INTERNAL_ERROR", "内部错误", nil)
			return
		}
	}
	now := s.now()
	t := v3db.Template{
		Key: in.Key, Name: in.Name, Item: in.Item, Tag: in.Tag,
		SceneContext: in.Scene, Kind: in.Kind,
		SHA256: hash, IconSHA256: hash, Icon: raw,
		Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	err = s.db.WithContext(r.Context()).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "key"}},
		DoUpdates: clause.Assignments(map[string]any{
			"name": in.Name, "item": in.Item, "tag": in.Tag,
			"scene_context": in.Scene, "kind": in.Kind,
			"sha256": hash, "icon_sha256": hash, "icon": raw,
			"enabled": true, "updated_at": now,
		}),
	}).Create(&t).Error
	if err != nil {
		s.logger.Error("模板写入失败", "err", err)
		writeErr(w, http.StatusInternalServerError, "INTERNAL_ERROR", "内部错误", nil)
		return
	}
	s.invalidateKnown()
	var saved v3db.Template
	if err := s.db.WithContext(r.Context()).Where("key = ?", in.Key).Take(&saved).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL_ERROR", "内部错误", nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema_version": 3, "template": toView(saved)})
}

func validateTemplateInput(in templateInput) error {
	if !keyRE.MatchString(in.Key) {
		return fmt.Errorf("key 必须是 1–128 位字母、数字、下划线、点或连字符")
	}
	if strings.TrimSpace(in.Name) == "" || len(in.Name) > 128 {
		return fmt.Errorf("name 不能为空且不超过 128 字符")
	}
	if strings.TrimSpace(in.Item) == "" || len(in.Item) > 128 {
		return fmt.Errorf("item 不能为空且不超过 128 字符")
	}
	if len(in.Tag) > 32 {
		return fmt.Errorf("tag 不超过 32 字符")
	}
	switch in.Scene {
	case "all", v3db.SceneCommission, v3db.SceneResearch:
	default:
		return fmt.Errorf("scene 必须为 all、commission 或 research")
	}
	switch in.Kind {
	case v3db.KindFormal, v3db.KindTemporary:
	default:
		return fmt.Errorf("kind 必须为 formal 或 temporary")
	}
	if in.ImageBase64 == "" {
		return fmt.Errorf("image_base64 不能为空")
	}
	return nil
}

// handleUpdateTemplate 只修改元数据，不重传图标；用于改名、标签、场景与启停。
func (s *Server) handleUpdateTemplate(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !keyRE.MatchString(key) {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "无效的 key", map[string]any{"field": "key"})
		return
	}
	var in struct {
		Name    *string `json:"name"`
		Item    *string `json:"item"`
		Tag     *string `json:"tag"`
		Scene   *string `json:"scene"`
		Kind    *string `json:"kind"`
		Enabled *bool   `json:"enabled"`
	}
	if err := decodeBody(w, r, 8<<10, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "INVALID_JSON", "请求体不是合法的 JSON", nil)
		return
	}
	updates := map[string]any{"updated_at": s.now()}
	if in.Name != nil {
		v := strings.TrimSpace(*in.Name)
		if v == "" || len(v) > 128 {
			writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "name 不能为空且不超过 128 字符", map[string]any{"field": "name"})
			return
		}
		updates["name"] = v
	}
	if in.Item != nil {
		v := strings.TrimSpace(*in.Item)
		if v == "" || len(v) > 128 {
			writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "item 不能为空且不超过 128 字符", map[string]any{"field": "item"})
			return
		}
		updates["item"] = v
	}
	if in.Tag != nil {
		v := strings.TrimSpace(*in.Tag)
		if len(v) > 32 {
			writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "tag 不超过 32 字符", map[string]any{"field": "tag"})
			return
		}
		updates["tag"] = v
	}
	if in.Scene != nil {
		switch *in.Scene {
		case "all", v3db.SceneCommission, v3db.SceneResearch:
		default:
			writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "scene 必须为 all、commission 或 research", map[string]any{"field": "scene"})
			return
		}
		updates["scene_context"] = *in.Scene
	}
	if in.Kind != nil {
		switch *in.Kind {
		case v3db.KindFormal, v3db.KindTemporary:
		default:
			writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "kind 必须为 formal 或 temporary", map[string]any{"field": "kind"})
			return
		}
		updates["kind"] = *in.Kind
	}
	if in.Enabled != nil {
		updates["enabled"] = *in.Enabled
	}
	if len(updates) == 1 {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "没有需要更新的字段", nil)
		return
	}
	res := s.db.WithContext(r.Context()).Model(&v3db.Template{}).Where("key = ?", key).Updates(updates)
	if res.Error != nil {
		s.logger.Error("模板更新失败", "key", key, "err", res.Error)
		writeErr(w, http.StatusInternalServerError, "INTERNAL_ERROR", "内部错误", nil)
		return
	}
	if res.RowsAffected == 0 {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "模板不存在", nil)
		return
	}
	s.invalidateKnown()
	var saved v3db.Template
	if err := s.db.WithContext(r.Context()).Where("key = ?", key).Take(&saved).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL_ERROR", "内部错误", nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema_version": 3, "template": toView(saved)})
}

// handleRenameTemplate 修改模板 ID（key），发布后新 key 会进入 manifest。
func (s *Server) handleRenameTemplate(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !keyRE.MatchString(key) {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "无效的 key", map[string]any{"field": "key"})
		return
	}
	var in struct {
		NewKey string `json:"new_key"`
	}
	if err := decodeBody(w, r, 4<<10, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "INVALID_JSON", "请求体不是合法的 JSON", nil)
		return
	}
	newKey := strings.TrimSpace(in.NewKey)
	if !keyRE.MatchString(newKey) {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "new_key 必须是 1–128 位字母、数字、下划线、点或连字符", map[string]any{"field": "new_key"})
		return
	}
	if newKey == key {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "new_key 与当前 key 相同", map[string]any{"field": "new_key"})
		return
	}
	var dup v3db.Template
	err := s.db.WithContext(r.Context()).Select("key").Where("key = ?", newKey).Take(&dup).Error
	if err == nil {
		writeErr(w, http.StatusConflict, "TEMPLATE_EXISTS", "目标 key 已存在", map[string]any{"existing_key": dup.Key})
		return
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		writeErr(w, http.StatusInternalServerError, "INTERNAL_ERROR", "内部错误", nil)
		return
	}
	res := s.db.WithContext(r.Context()).Model(&v3db.Template{}).
		Where("key = ?", key).
		Updates(map[string]any{"key": newKey, "updated_at": s.now()})
	if res.Error != nil {
		s.logger.Error("模板改名失败", "key", key, "new_key", newKey, "err", res.Error)
		writeErr(w, http.StatusInternalServerError, "INTERNAL_ERROR", "内部错误", nil)
		return
	}
	if res.RowsAffected == 0 {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "模板不存在", nil)
		return
	}
	s.invalidateKnown()
	var saved v3db.Template
	if err := s.db.WithContext(r.Context()).Where("key = ?", newKey).Take(&saved).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL_ERROR", "内部错误", nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema_version": 3, "old_key": key, "template": toView(saved)})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	var state v3db.TemplateState
	if err := s.db.WithContext(r.Context()).Where("id = ?", 1).Take(&state).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL_ERROR", "内部错误", nil)
		return
	}
	var enabled int64
	if err := s.db.WithContext(r.Context()).Model(&v3db.Template{}).
		Where("enabled = ?", true).Count(&enabled).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL_ERROR", "内部错误", nil)
		return
	}
	var pending int64
	if err := s.db.WithContext(r.Context()).Model(&v3db.UnknownTemplate{}).
		Where("status = ?", unknownPending).Count(&pending).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL_ERROR", "内部错误", nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"schema_version":   3,
		"revision":         state.CurrentRevision,
		"public_base":      state.PublicBase,
		"worker_name":      s.cfg.WorkerName,
		"cf_deploy":        s.cfg.CFDeploy,
		"enabled":          enabled,
		"pending_clusters": pending,
	})
}

func (s *Server) handleDeleteTemplate(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !keyRE.MatchString(key) {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "无效的 key", map[string]any{"field": "key"})
		return
	}
	res := s.db.WithContext(r.Context()).Model(&v3db.Template{}).
		Where("key = ?", key).
		Updates(map[string]any{"enabled": false, "updated_at": s.now()})
	if res.Error != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL_ERROR", "内部错误", nil)
		return
	}
	if res.RowsAffected == 0 {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "模板不存在", nil)
		return
	}
	s.invalidateKnown()
	writeJSON(w, http.StatusOK, map[string]any{"schema_version": 3, "disabled": key})
}

func (s *Server) handleListTemplates(w http.ResponseWriter, r *http.Request) {
	q := s.db.WithContext(r.Context()).Model(&v3db.Template{})
	values := r.URL.Query()
	if v := values.Get("enabled"); v == "true" || v == "false" {
		q = q.Where("enabled = ?", v == "true")
	}
	if v := values.Get("scene"); v != "" {
		q = q.Where("scene_context = ?", v)
	}
	if v := values.Get("item"); v != "" {
		q = q.Where("item = ?", v)
	}
	limit := 500
	if v := values.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 2000 {
			limit = n
		}
	}
	var rows []v3db.Template
	if err := q.Order("key ASC").Limit(limit).Find(&rows).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL_ERROR", "内部错误", nil)
		return
	}
	out := make([]templateView, 0, len(rows))
	for _, row := range rows {
		out = append(out, toView(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema_version": 3, "templates": out})
}

type catalogInput struct {
	Commissions []struct {
		Comm              string `json:"comm"`
		Name              string `json:"name"`
		DurationSeconds   int    `json:"duration_seconds"`
		ExpirationSeconds int    `json:"expiration_seconds"`
		Code              string `json:"code"`
	} `json:"commissions"`
}

func (s *Server) handleImportCatalog(w http.ResponseWriter, r *http.Request) {
	var in catalogInput
	if err := decodeBody(w, r, 2<<20, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "INVALID_JSON", "请求体不是合法的 JSON", nil)
		return
	}
	if len(in.Commissions) == 0 || len(in.Commissions) > 6000 {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "commissions 数量必须在 1–6000 之间", nil)
		return
	}
	rows := make([]v3db.CommissionCatalog, 0, len(in.Commissions))
	for _, c := range in.Commissions {
		if strings.TrimSpace(c.Comm) == "" || len(c.Comm) > 128 || len(c.Name) > 128 || len(c.Code) > 32 {
			writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "委托字段长度非法", map[string]any{"comm": c.Comm})
			return
		}
		if c.DurationSeconds < 0 || c.DurationSeconds > 86400 || c.ExpirationSeconds < 0 || c.ExpirationSeconds > 86400*30 {
			writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "委托时长非法", map[string]any{"comm": c.Comm})
			return
		}
		rows = append(rows, v3db.CommissionCatalog{
			Comm: c.Comm, Name: c.Name, DurationSeconds: c.DurationSeconds,
			ExpirationSeconds: c.ExpirationSeconds, Code: c.Code, Active: true,
		})
	}
	err := s.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "comm"}},
			DoUpdates: clause.AssignmentColumns([]string{"name", "duration_seconds", "expiration_seconds", "code", "active"}),
		}).CreateInBatches(rows, 500).Error
	})
	if err != nil {
		s.logger.Error("委托目录导入失败", "err", err)
		writeErr(w, http.StatusInternalServerError, "INTERNAL_ERROR", "内部错误", nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema_version": 3, "imported": len(rows)})
}

func (s *Server) handlePublish(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Note string `json:"note"`
	}
	if r.ContentLength > 0 {
		if err := decodeBody(w, r, 8<<10, &in); err != nil {
			writeErr(w, http.StatusBadRequest, "INVALID_JSON", "请求体不是合法的 JSON", nil)
			return
		}
	}
	if len(in.Note) > 256 {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", "note 不超过 256 字符", nil)
		return
	}
	rel, err := s.publish(r.Context(), in.Note)
	if err != nil {
		s.logger.Error("模板发布失败", "err", err)
		status := http.StatusInternalServerError
		code := "PUBLISH_FAILED"
		if rel != nil {
			status = http.StatusBadGateway
		}
		writeErr(w, status, code, err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"schema_version":  3,
		"revision":        rel.Revision,
		"manifest_sha256": rel.ManifestSHA256,
		"signature":       rel.Signature,
		"deployed":        rel.Deployed,
		"worker_url":      rel.WorkerURL,
	})
}

func (s *Server) handleReleases(w http.ResponseWriter, r *http.Request) {
	var rows []v3db.ContentRelease
	if err := s.db.WithContext(r.Context()).
		Select("revision, created_at, note, manifest_sha256, signature, deployed, worker_url").
		Order("revision DESC").Limit(100).Find(&rows).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL_ERROR", "内部错误", nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema_version": 3, "releases": rows})
}

// publish 在持有状态行锁的情况下分配 revision、构建产物并部署。
// 部署失败时仍保留一条 deployed=false 的发布记录，便于排查。
func (s *Server) publish(ctx context.Context, note string) (*v3db.ContentRelease, error) {
	var out v3db.ContentRelease
	var deployErr error
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var state v3db.TemplateState
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", 1).Take(&state).Error; err != nil {
			return err
		}
		var maxRev uint64
		if err := tx.Model(&v3db.ContentRelease{}).Select("COALESCE(MAX(revision),0)").Scan(&maxRev).Error; err != nil {
			return err
		}
		revision := state.CurrentRevision
		if maxRev > revision {
			revision = maxRev
		}
		revision++

		var templates []v3db.Template
		if err := tx.Where("enabled = ?", true).Order("key ASC").Find(&templates).Error; err != nil {
			return err
		}
		if len(templates) == 0 {
			return fmt.Errorf("没有启用模板，拒绝发布")
		}
		var prev []v3db.ContentRelease
		if err := tx.Select("revision, catalog").Where("revision < ?", revision).
			Order("revision DESC").Limit(50).Find(&prev).Error; err != nil {
			return err
		}
		prevCatalogs := make(map[uint64][]CatalogEntry, len(prev))
		for _, rel := range prev {
			var cat []CatalogEntry
			if len(rel.Catalog) > 0 {
				_ = json.Unmarshal(rel.Catalog, &cat)
			}
			prevCatalogs[rel.Revision] = cat
		}
		mappings, err := s.mappingsJSON(tx, revision)
		if err != nil {
			return err
		}
		plan, err := BuildRelease(revision, templates, prevCatalogs, mappings, s.now(), s.signer, s.cfg.ExpiresDays)
		if err != nil {
			return err
		}
		rel := v3db.ContentRelease{
			Revision: revision, CreatedAt: s.now(), Note: note,
			ManifestSHA256: plan.ManifestSHA256, Signature: plan.Signature,
			MatchThresholds: v3db.JSONB(`{"formal":0.92,"cluster":0.96,"margin":0.03}`),
			Catalog:         v3db.JSONB(plan.CatalogJSON),
			Deployed:        false,
		}
		if err := tx.Create(&rel).Error; err != nil {
			return err
		}
		workerURL, err := s.publisher.Deploy(ctx, plan)
		if err != nil {
			deployErr = err
			out = rel
			return nil
		}
		if err := tx.Model(&v3db.ContentRelease{}).Where("revision = ?", revision).
			Updates(map[string]any{"deployed": true, "worker_url": workerURL}).Error; err != nil {
			return err
		}
		if err := tx.Model(&v3db.TemplateState{}).Where("id = ?", 1).
			Update("current_revision", revision).Error; err != nil {
			return err
		}
		rel.Deployed = true
		rel.WorkerURL = workerURL
		out = rel
		return nil
	})
	if err != nil {
		return nil, err
	}
	if deployErr != nil {
		return &out, deployErr
	}
	return &out, nil
}

type mappingView struct {
	ClusterKey string `json:"cluster_key"`
	Item       string `json:"item"`
	Tag        string `json:"tag"`
}
type aliasView struct {
	FromKey string `json:"from_key"`
	ToKey   string `json:"to_key"`
}

func (s *Server) mappingsJSON(tx *gorm.DB, revision uint64) ([]byte, error) {
	var mappings []v3db.UnknownMapping
	if err := tx.Order("revision ASC, id ASC").Find(&mappings).Error; err != nil {
		return nil, err
	}
	var aliases []v3db.UnknownClusterAlias
	if err := tx.Order("from_key ASC").Find(&aliases).Error; err != nil {
		return nil, err
	}
	var rejected []string
	if err := tx.Model(&v3db.UnknownTemplate{}).Where("status = ?", unknownRejected).
		Pluck("cluster_key", &rejected).Error; err != nil {
		return nil, err
	}
	skip := make(map[string]bool, len(rejected))
	for _, key := range rejected {
		skip[key] = true
	}
	latest := make(map[string]v3db.UnknownMapping, len(mappings))
	for _, m := range mappings {
		if m.Revision <= revision && !skip[m.ClusterKey] {
			latest[m.ClusterKey] = m
		}
	}
	outMappings := make([]mappingView, 0, len(latest))
	for _, m := range latest {
		outMappings = append(outMappings, mappingView{ClusterKey: m.ClusterKey, Item: m.Item, Tag: m.Tag})
	}
	outAliases := make([]aliasView, 0, len(aliases))
	for _, a := range aliases {
		outAliases = append(outAliases, aliasView{FromKey: a.FromKey, ToKey: a.ToKey})
	}
	return json.Marshal(map[string]any{
		"schema_version": 3,
		"revision":       revision,
		"mappings":       outMappings,
		"aliases":        outAliases,
	})
}

// ---- 小工具 ----

func decodeBody(w http.ResponseWriter, r *http.Request, max int64, v any) error {
	body := http.MaxBytesReader(w, r.Body, max)
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return fmt.Errorf("body too large")
		}
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, message string, details map[string]any) {
	if details == nil {
		details = map[string]any{}
	}
	writeJSON(w, status, map[string]any{
		"schema_version": 3,
		"error":          map[string]any{"code": code, "message": message, "details": details},
	})
}
