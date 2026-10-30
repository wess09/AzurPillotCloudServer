package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"ap-cloud-server/internal/adminws"
	"ap-cloud-server/internal/maintenance"
	"ap-cloud-server/internal/store"
)

func managementError(value apiError) error {
	return adminws.Fault{Code: value.code, Message: value.message, Status: value.status}
}
func decodeCommand(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(new(any)) != io.EOF {
		return managementError(errInvalidJSON())
	}
	return nil
}
func (s *Server) adminSnapshot(ctx context.Context) (any, error) {
	announcement, err := s.store.LatestAnnouncement(ctx)
	if err != nil {
		return nil, managementError(errInternal())
	}
	force, err := s.store.GetUpdatePolicy(ctx)
	if err != nil {
		return nil, managementError(errInternal())
	}
	plans, err := s.store.ListMaintenances(ctx)
	if err != nil {
		return nil, managementError(errInternal())
	}
	history, err := s.store.LatestPublishByWorker(ctx)
	if err != nil {
		return nil, managementError(errInternal())
	}
	var source any
	if s.deps.Maintenance != nil {
		state, e := s.deps.Maintenance.Status(ctx)
		if e == nil {
			source = state
		}
	}
	return map[string]any{"announcement": announcement, "force_update": force, "maintenances": plans, "publish_history": history, "maintenance_source": source}, nil
}

func (s *Server) executeAdmin(ctx context.Context, method string, params json.RawMessage) (any, error) {
	internal := func(err error) error {
		if err != nil {
			s.logger.Error("management operation failed", "method", method, "err", err)
			return managementError(errInternal())
		}
		return nil
	}
	switch method {
	case "announcement.get":
		value, err := s.store.LatestAnnouncement(ctx)
		return map[string]any{"announcement": value}, internal(err)
	case "announcement.publish":
		var req announcementRequest
		if err := decodeCommand(params, &req); err != nil {
			return nil, err
		}
		if strings.TrimSpace(req.Title) == "" {
			return nil, managementError(errValidation("title 不能为空", "title"))
		}
		if (req.Content == nil || strings.TrimSpace(*req.Content) == "") && (req.URL == nil || strings.TrimSpace(*req.URL) == "") {
			return nil, managementError(errValidation("content 与 url 至少一项有内容", "content"))
		}
		id, err := s.store.GenerateAnnouncementID(ctx, s.now())
		if err != nil {
			return nil, internal(err)
		}
		if err = s.store.SetAnnouncement(ctx, store.Announcement{ID: id, Title: req.Title, Content: req.Content, URL: req.URL}); err != nil {
			return nil, internal(err)
		}
		stored, err := s.store.LatestAnnouncement(ctx)
		if err != nil || stored == nil {
			return nil, managementError(errInternal())
		}
		s.adminSocket.Notify()
		return map[string]any{"announcement": stored, "publish": publishResultJSON(s.publishNotice(ctx))}, nil
	case "announcement.withdraw":
		if err := s.store.WithdrawAnnouncement(ctx); err != nil {
			return nil, internal(err)
		}
		s.adminSocket.Notify()
		return map[string]any{"publish": publishResultJSON(s.publishNotice(ctx))}, nil
	case "update_policy.get":
		value, err := s.store.GetUpdatePolicy(ctx)
		return map[string]any{"force_update": value}, internal(err)
	case "update_policy.set":
		var req updatePolicyRequest
		if err := decodeCommand(params, &req); err != nil {
			return nil, err
		}
		if req.ForceUpdate == nil {
			return nil, managementError(errValidation("force_update 必须为布尔值", "force_update"))
		}
		if err := s.store.SetUpdatePolicy(ctx, *req.ForceUpdate); err != nil {
			return nil, internal(err)
		}
		s.adminSocket.Notify()
		return map[string]any{"force_update": *req.ForceUpdate, "publish": publishResultJSON(s.publishUpdate(ctx))}, nil
	case "maintenance.list":
		value, err := s.store.ListMaintenances(ctx)
		return map[string]any{"maintenances": value}, internal(err)
	case "maintenance.source", "maintenance.sync":
		if s.deps.Maintenance == nil {
			return nil, managementError(errServiceUnavailable())
		}
		if method == "maintenance.sync" {
			s.adminSocket.Notify()
			if err := s.deps.Maintenance.Sync(ctx); err != nil {
				if errors.Is(err, maintenance.ErrBusy) {
					return nil, managementError(errRequestConflict(err.Error()))
				}
				return nil, managementError(errSourceUnavailable())
			}
		}
		value, err := s.deps.Maintenance.Status(ctx)
		return value, internal(err)
	case "maintenance.save":
		var req struct {
			Server   string  `json:"server"`
			Name     string  `json:"name"`
			StartsAt string  `json:"starts_at"`
			EndsAt   *string `json:"ends_at"`
		}
		if err := decodeCommand(params, &req); err != nil {
			return nil, err
		}
		if !validMaintenanceServer(req.Server) {
			return nil, managementError(errValidation("server 取值不合法", "server"))
		}
		if strings.TrimSpace(req.Name) == "" {
			return nil, managementError(errValidation("name 不能为空", "name"))
		}
		start, err := time.Parse(time.RFC3339, req.StartsAt)
		if err != nil {
			return nil, managementError(errValidation("starts_at 必须是带时区的 RFC3339 时间", "starts_at"))
		}
		var end *time.Time
		if req.EndsAt != nil && *req.EndsAt != "" {
			parsed, e := time.Parse(time.RFC3339, *req.EndsAt)
			if e != nil || parsed.Before(start) {
				return nil, managementError(errValidation("ends_at 必须是合法时间且不得早于 starts_at", "ends_at"))
			}
			end = &parsed
		}
		if err = s.store.UpsertMaintenance(ctx, store.Maintenance{Server: req.Server, Name: req.Name, StartsAt: start, EndsAt: end}); err != nil {
			return nil, internal(err)
		}
		return map[string]bool{"saved": true}, nil
	case "maintenance.delete":
		var req struct {
			Server string `json:"server"`
		}
		if err := decodeCommand(params, &req); err != nil {
			return nil, err
		}
		if !validMaintenanceServer(req.Server) {
			return nil, managementError(errValidation("server 取值不合法", "server"))
		}
		value, err := s.store.DeleteMaintenance(ctx, req.Server)
		return map[string]bool{"deleted": value}, internal(err)
	case "publish.status":
		value, err := s.store.LatestPublishByWorker(ctx)
		return map[string]any{"publish_history": value}, internal(err)
	default:
		return nil, adminws.Fault{Code: "METHOD_NOT_FOUND", Message: "管理命令不存在", Status: 404}
	}
}

func validMaintenanceServer(server string) bool {
	switch server {
	case "cn", "en", "jp", "tw":
		return true
	default:
		return false
	}
}
