package statistics

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

var legacyDevicePattern = regexp.MustCompile(`^[0-9a-fA-F]{32,64}$`)

// Historical AP module/statistics/azurstats.py paginates individual item rows
// and counts combat_count once per imgid. Read only current authenticated OCR.
func (s *Service) legacyOpsiItems(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		fail(w, 405, "METHOD_NOT_ALLOWED", "GET required")
		return
	}
	q := r.URL.Query()
	device := strings.ToLower(q.Get("device_id"))
	if !legacyDevicePattern.MatchString(device) {
		fail(w, 400, "VALIDATION_ERROR", "device_id is required")
		return
	}
	limit, err := legacyLimit(r, "limit", 1000, 1000)
	if err != nil {
		fail(w, 400, "VALIDATION_ERROR", err.Error())
		return
	}
	offset := 0
	if v := q.Get("offset"); v != "" {
		offset, err = strconv.Atoi(v)
		if err != nil || offset < 0 || offset > 10000000 {
			fail(w, 400, "VALIDATION_ERROR", "invalid offset")
			return
		}
	}
	genre := q.Get("genre")
	if genre == "" {
		genre = "opsi_meowfficer_farming"
	}
	rows, err := s.Pool.Query(r.Context(), `SELECT r.sha256 AS imgid,e.server,
	COALESCE(r.payload->'events'->e.ordinal->>'zone','') AS zone,
	COALESCE(r.payload->'events'->e.ordinal->>'zone_type','UNKNOWN') AS zone_type,
	COALESCE((r.payload->'events'->e.ordinal->>'zone_id')::integer,0) AS zone_id,
	COALESCE((r.payload->'events'->e.ordinal->>'hazard_level')::integer,0) AS hazard_level,
	COALESCE((r.payload#>>'{source,metadata,combat_count}')::bigint,0) AS combat_count,
	i.item,i.amount,i.tag,e.occurred_at AS created_at
	FROM stat_effective_results r JOIN stat_effective_events e ON e.sha256=r.sha256 JOIN stat_effective_items i ON i.sha256=e.sha256 AND i.ordinal=e.ordinal
	WHERE r.status='complete' AND e.kind='drop' AND e.scene=$2 AND e.scene LIKE 'opsi\_%' ESCAPE '\'
	AND COALESCE(NULLIF(r.payload#>>'{source,metadata,legacy_device_id}',''),r.payload#>>'{source,metadata,device_id}')=$1
	ORDER BY e.occurred_at,r.sha256,e.ordinal,i.item,i.tag LIMIT $3 OFFSET $4`, device, genre, limit, offset)
	if err != nil {
		internal(w, err)
		return
	}
	data, err := collect(rows)
	if err != nil {
		internal(w, err)
		return
	}
	output(w, 200, map[string]any{"data": data, "limit": limit, "offset": offset})
}

func legacyLimit(r *http.Request, key string, fallback, maximum int) (int, error) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("invalid %s", key)
	}
	return min(n, maximum), nil
}

func legacyPublic(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		next(w, r)
	}
}
