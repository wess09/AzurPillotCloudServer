package httpapi

import (
	"context"
	"net/http"

	"ap-cloud-server/internal/store"
)

type cl1StatisticsReader interface {
	Cl1Statistics(context.Context) (store.Cl1Statistics, error)
}

func (s *Server) handleV3Cl1Statistics(w http.ResponseWriter, r *http.Request) {
	reader, ok := s.store.(cl1StatisticsReader)
	if !ok {
		writeV3Error(w, r, errServiceUnavailable())
		return
	}
	result, err := reader.Cl1Statistics(r.Context())
	if err != nil {
		s.logger.Error("CL1 statistics failed", "err", err)
		writeV3Error(w, r, errInternal())
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=30")
	writeJSON(w, r, http.StatusOK, map[string]any{"schema_version": 3, "result": result})
}
