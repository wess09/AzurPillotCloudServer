package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ap-cloud-server/internal/templatesync"
)

// The main admin session is checked locally; only the server-side service token goes upstream.
func (s *Server) handleTemplateAdmin(w http.ResponseWriter, r *http.Request) {
	base, err := url.Parse(s.deps.Cfg.TemplateAdminURL)
	if err != nil || base.Host == "" || base.User != nil || (base.Scheme != "http" && base.Scheme != "https") || s.deps.Cfg.TemplateAdminToken == "" {
		writeError(w, r, apiError{503, "TEMPLATE_SERVICE_UNAVAILABLE", "尚未配置独立模板服务", nil})
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/template-service")
	allowed := (r.Method == http.MethodGet && (path == "/status" || path == "/releases" || path == "/templates")) ||
		(r.Method == http.MethodPost && (path == "/templates" || path == "/publish")) ||
		(r.Method == http.MethodPut && strings.HasPrefix(path, "/mappings/") && !strings.Contains(strings.TrimPrefix(path, "/mappings/"), "/"))
	if !allowed {
		writeError(w, r, apiError{404, "NOT_FOUND", "模板管理操作不存在", nil})
		return
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/internal/v1/templates" + strings.TrimPrefix(path, "/templates")
	base.RawQuery = r.URL.RawQuery
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 512<<10))
	if err != nil {
		writeError(w, r, apiError{413, "PAYLOAD_TOO_LARGE", "模板请求过大", nil})
		return
	}
	request, err := http.NewRequestWithContext(r.Context(), r.Method, base.String(), strings.NewReader(string(body)))
	if err != nil {
		writeError(w, r, errInternal())
		return
	}
	request.Header.Set("Authorization", "Bearer "+s.deps.Cfg.TemplateAdminToken)
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		writeError(w, r, apiError{502, "TEMPLATE_SERVICE_UNAVAILABLE", "无法连接独立模板服务", nil})
		return
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
	if err != nil || len(data) > 8<<20 {
		writeError(w, r, errInternal())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(data)
}

func (s *Server) handleTemplateEvent(w http.ResponseWriter, r *http.Request) {
	token := s.deps.Cfg.TemplateEventToken
	if token == "" || s.deps.TemplateSync == nil {
		writeError(w, r, apiError{503, "TEMPLATE_SYNC_DISABLED", "模板同步未启用", nil})
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
		writeError(w, r, errUnauthorized())
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
	if err != nil {
		writeError(w, r, apiError{413, "PAYLOAD_TOO_LARGE", "模板事件过大", nil})
		return
	}
	var event templatesync.Event
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&event); err != nil {
		writeError(w, r, errInvalidJSON())
		return
	}
	if decoder.Decode(new(any)) != io.EOF {
		writeError(w, r, errInvalidJSON())
		return
	}
	if err := event.Validate(); err != nil {
		writeError(w, r, apiError{400, "INVALID_TEMPLATE_EVENT", err.Error(), nil})
		return
	}
	digest := sha256.Sum256(body)
	if err := s.deps.TemplateSync.Apply(r.Context(), event, hex.EncodeToString(digest[:])); err != nil {
		if errors.Is(err, templatesync.ErrConflict) {
			writeError(w, r, apiError{409, "EVENT_CONFLICT", err.Error(), nil})
			return
		}
		writeError(w, r, errInternal())
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"ack": event.EventID})
}
