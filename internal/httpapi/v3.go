package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"ap-cloud-server/internal/drops"
)

const (
	v3SchemaVersion = 3
	v3MaxBatch      = 200
	v3MaxBodyBytes  = 1 << 20
	v3MaxRange      = 366 * 24 * time.Hour
	v3DefaultRange  = 30 * 24 * time.Hour
)

// writeV3Error 输出 schema_version=3 的错误信封，复用统一状态码与细节。
func writeV3Error(w http.ResponseWriter, r *http.Request, e apiError) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if e.status == http.StatusMethodNotAllowed {
		if allow, ok := e.details["allow"].(string); ok && allow != "" {
			w.Header().Set("Allow", allow)
		}
	}
	if e.status == http.StatusTooManyRequests {
		if retry, ok := e.details["retry_after"].(string); ok && retry != "" {
			w.Header().Set("Retry-After", retry)
		}
	}
	w.WriteHeader(e.status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"schema_version": v3SchemaVersion,
		"error": map[string]any{
			"code":    e.code,
			"message": e.message,
			"details": e.details,
		},
	})
}

func (s *Server) handleV3Drops(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeV3Error(w, r, errMethodNotAllowed("POST"))
		return
	}
	if s.deps.Drops == nil {
		writeV3Error(w, r, errServiceUnavailable())
		return
	}
	if !isJSON(r.Header.Get("Content-Type")) {
		writeV3Error(w, r, errUnsupportedMediaType())
		return
	}
	body := http.MaxBytesReader(w, r.Body, v3MaxBodyBytes)
	dec := json.NewDecoder(body)
	var batch drops.Batch
	if err := dec.Decode(&batch); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeV3Error(w, r, apiError{http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "请求体超过 1 MiB 上限", map[string]any{}})
			return
		}
		writeV3Error(w, r, errInvalidJSON())
		return
	}
	if len(batch.Drops) == 0 {
		writeV3Error(w, r, errValidation("drops 不能为空", "drops"))
		return
	}
	if len(batch.Drops) > v3MaxBatch {
		writeV3Error(w, r, apiError{http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "单次最多上报 200 条", map[string]any{"limit": v3MaxBatch}})
		return
	}
	acks := s.deps.Drops.Submit(r.Context(), batch.Drops)
	writeJSON(w, r, http.StatusOK, map[string]any{
		"schema_version": v3SchemaVersion,
		"revision":       s.templateRevision(r),
		"results":        acks,
	})
}

// Template revisions come exclusively from received mapping events; never fetch template resources.
func (s *Server) templateRevision(r *http.Request) uint64 {
	if s.deps.TemplateSync != nil {
		if revision, err := s.deps.TemplateSync.Revision(r.Context()); err == nil {
			return revision
		}
	}
	return 0
}

func (s *Server) handleV3Commission(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeV3Error(w, r, errMethodNotAllowed("GET"))
		return
	}
	if s.deps.Drops == nil {
		writeV3Error(w, r, errServiceUnavailable())
		return
	}
	q, from, to, tz, apiErr := parseV3Query(r)
	if apiErr != nil {
		writeV3Error(w, r, *apiErr)
		return
	}
	status, apiErr := parseStatus(r.URL.Query().Get("status"))
	if apiErr != nil {
		writeV3Error(w, r, *apiErr)
		return
	}
	groups, err := s.deps.Drops.Commission(r.Context(), drops.CommissionQuery{
		From: from, To: to, Server: q.Get("server"), Comm: q.Get("comm"), Status: status,
	})
	if err != nil {
		if errors.Is(err, drops.ErrInvalidRange) {
			writeV3Error(w, r, errValidation("时间范围无效", "from/to"))
			return
		}
		s.logger.Error("v3 委托查询失败", "err", err)
		writeV3Error(w, r, errInternal())
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{
		"schema_version": v3SchemaVersion,
		"from":           from.UTC().Format(time.RFC3339),
		"to":             to.UTC().Format(time.RFC3339),
		"timezone":       tz,
		"groups":         groups,
	})
}

func (s *Server) handleV3Research(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeV3Error(w, r, errMethodNotAllowed("GET"))
		return
	}
	if s.deps.Drops == nil {
		writeV3Error(w, r, errServiceUnavailable())
		return
	}
	q, from, to, tz, apiErr := parseV3Query(r)
	if apiErr != nil {
		writeV3Error(w, r, *apiErr)
		return
	}
	series, apiErr := parsePositiveInt(q.Get("series"), 0, 100, "series")
	if apiErr != nil {
		writeV3Error(w, r, *apiErr)
		return
	}
	groups, err := s.deps.Drops.Research(r.Context(), drops.ResearchQuery{
		From: from, To: to, Server: q.Get("server"), Series: series,
		Project: q.Get("project"), Item: q.Get("item"), Tag: q.Get("tag"),
	})
	if err != nil {
		if errors.Is(err, drops.ErrInvalidRange) {
			writeV3Error(w, r, errValidation("时间范围无效", "from/to"))
			return
		}
		s.logger.Error("v3 科研查询失败", "err", err)
		writeV3Error(w, r, errInternal())
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{
		"schema_version": v3SchemaVersion,
		"from":           from.UTC().Format(time.RFC3339),
		"to":             to.UTC().Format(time.RFC3339),
		"timezone":       tz,
		"groups":         groups,
	})
}

func (s *Server) handleV3CommissionOptions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeV3Error(w, r, errMethodNotAllowed("GET"))
		return
	}
	if s.deps.Drops == nil {
		writeV3Error(w, r, errServiceUnavailable())
		return
	}
	q, from, to, _, apiErr := parseV3Query(r)
	if apiErr != nil {
		writeV3Error(w, r, *apiErr)
		return
	}
	options, err := s.deps.Drops.CommissionOptions(r.Context(), from, to, q.Get("server"))
	if err != nil {
		s.logger.Error("v3 委托选项查询失败", "err", err)
		writeV3Error(w, r, errInternal())
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{
		"schema_version": v3SchemaVersion,
		"from":           from.UTC().Format(time.RFC3339),
		"to":             to.UTC().Format(time.RFC3339),
		"options":        options,
	})
}

func (s *Server) handleV3ResearchOptions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeV3Error(w, r, errMethodNotAllowed("GET"))
		return
	}
	if s.deps.Drops == nil {
		writeV3Error(w, r, errServiceUnavailable())
		return
	}
	q, from, to, _, apiErr := parseV3Query(r)
	if apiErr != nil {
		writeV3Error(w, r, *apiErr)
		return
	}
	options, err := s.deps.Drops.ResearchOptions(r.Context(), from, to, q.Get("server"))
	if err != nil {
		s.logger.Error("v3 科研选项查询失败", "err", err)
		writeV3Error(w, r, errInternal())
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{
		"schema_version": v3SchemaVersion,
		"from":           from.UTC().Format(time.RFC3339),
		"to":             to.UTC().Format(time.RFC3339),
		"options":        options,
	})
}

// parseV3Query 解析公共时间范围。默认最近 30 天，最大 366 天。
func parseV3Query(r *http.Request) (url.Values, time.Time, time.Time, string, *apiError) {
	q := r.URL.Query()
	tz := strings.TrimSpace(q.Get("timezone"))
	if tz == "" {
		tz = "UTC"
	}
	if _, err := time.LoadLocation(tz); err != nil {
		e := errValidation("无效的时区", "timezone")
		return q, time.Time{}, time.Time{}, "", &e
	}
	now := time.Now().UTC()
	var from, to time.Time
	var err error
	if v := strings.TrimSpace(q.Get("to")); v != "" {
		to, err = time.Parse(time.RFC3339, v)
		if err != nil {
			e := errValidation("to 必须是 RFC3339 时间", "to")
			return q, time.Time{}, time.Time{}, "", &e
		}
	}
	if v := strings.TrimSpace(q.Get("from")); v != "" {
		from, err = time.Parse(time.RFC3339, v)
		if err != nil {
			e := errValidation("from 必须是 RFC3339 时间", "from")
			return q, time.Time{}, time.Time{}, "", &e
		}
	}
	switch {
	case from.IsZero() && to.IsZero():
		to = now
		from = to.Add(-v3DefaultRange)
	case from.IsZero():
		from = to.Add(-v3DefaultRange)
	case to.IsZero():
		to = from.Add(v3DefaultRange)
	}
	if !to.After(from) {
		e := errValidation("to 必须晚于 from", "from/to")
		return q, time.Time{}, time.Time{}, "", &e
	}
	if to.Sub(from) > v3MaxRange {
		e := errValidation("查询范围不能超过 366 天", "from/to")
		return q, time.Time{}, time.Time{}, "", &e
	}
	return q, from.UTC(), to.UTC(), tz, nil
}

func parseStatus(raw string) (*int, *apiError) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || (n != 0 && n != 1) {
		e := errValidation("status 只能为 0 或 1", "status")
		return nil, &e
	}
	return &n, nil
}

func parsePositiveInt(raw string, min, max int, field string) (int, *apiError) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < min || n > max {
		e := errValidation("数值超出范围", field)
		return 0, &e
	}
	return n, nil
}

func isJSON(contentType string) bool {
	ct := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	return ct == "application/json"
}
