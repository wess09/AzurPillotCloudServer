package httpapi

import (
	"ap-cloud-server/internal/drops"
	"errors"
	"net/http"
)

func (s *Server) handleV3Items(w http.ResponseWriter, r *http.Request) {
	if s.deps.Drops == nil {
		writeV3Error(w, r, errServiceUnavailable())
		return
	}
	items, err := s.deps.Drops.CatalogItems(r.Context())
	if err != nil {
		writeV3Error(w, r, errInternal())
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"schema_version": 3, "items": items})
}

func (s *Server) handleV3Analysis(w http.ResponseWriter, r *http.Request) {
	if s.deps.Drops == nil {
		writeV3Error(w, r, errServiceUnavailable())
		return
	}
	q, from, to, _, apiErr := parseV3Query(r)
	if apiErr != nil {
		writeV3Error(w, r, *apiErr)
		return
	}
	series, apiErr := parsePositiveInt(q.Get("series"), 0, 100, "series")
	if apiErr != nil {
		writeV3Error(w, r, *apiErr)
		return
	}
	duration, apiErr := parsePositiveInt(q.Get("duration"), 0, 604800, "duration")
	if apiErr != nil {
		writeV3Error(w, r, *apiErr)
		return
	}
	groupBy := q.Get("group_by")
	if groupBy == "" {
		groupBy = "project"
	}
	result, err := s.deps.Drops.Analysis(r.Context(), drops.AnalysisQuery{From: from, To: to, Scene: q.Get("scene"), Server: q.Get("server"), Target: q.Get("target"), GroupBy: groupBy, Series: series, Duration: duration, Category: q.Get("category")})
	if err != nil {
		if errors.Is(err, drops.ErrAnalysisQuery) {
			writeV3Error(w, r, errValidation("无效的统计类型、产物、分组或筛选条件", "query"))
			return
		}
		s.logger.Error("v3 产物分析失败", "err", err)
		writeV3Error(w, r, errInternal())
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"schema_version": 3, "from": from, "to": to, "scene": q.Get("scene"), "target": q.Get("target"), "result": result})
}
