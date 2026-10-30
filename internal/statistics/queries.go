package statistics

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/jackc/pgx/v5"
)

// Bound response memory without rejecting ordinary cross-series summaries.
// This counts aggregate rows, not source screenshots or individual events.
const maxSummaryRows = 100000

func collect(rows pgx.Rows) ([]map[string]any, error) {
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		v, e := rows.Values()
		if e != nil {
			return nil, e
		}
		m := map[string]any{}
		for i, f := range rows.FieldDescriptions() {
			m[f.Name] = v[i]
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
func (s *Service) summary(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	for k, v := range q {
		if len(v) != 1 || !strings.Contains("|from|to|server|scene|series|project|focus_series|timezone|comm|status|", "|"+k+"|") {
			fail(w, 400, "VALIDATION_ERROR", "无效筛选参数")
			return
		}
	}
	to := s.Now().UTC()
	var e error
	if q.Get("to") != "" {
		to, e = time.Parse(time.RFC3339Nano, q.Get("to"))
		if e != nil {
			fail(w, 400, "VALIDATION_ERROR", "to 需要带时区")
			return
		}
	}
	from := to.AddDate(0, 0, -30)
	if q.Get("from") != "" {
		from, e = time.Parse(time.RFC3339Nano, q.Get("from"))
		if e != nil {
			fail(w, 400, "VALIDATION_ERROR", "from 需要带时区")
			return
		}
	}
	series := 0
	if q.Get("series") != "" {
		series, e = strconv.Atoi(q.Get("series"))
		if e != nil || series < 1 || series > 100 {
			fail(w, 400, "VALIDATION_ERROR", "无效期数")
			return
		}
	}
	focus := -1
	if q.Get("focus_series") == "unknown" {
		focus = 0
	} else if q.Get("focus_series") != "" {
		focus, e = strconv.Atoi(q.Get("focus_series"))
		if e != nil || focus < 1 || focus > 100 {
			fail(w, 400, "VALIDATION_ERROR", "无效科研倾向期数")
			return
		}
	}
	zone := q.Get("timezone")
	if zone == "" {
		// Preserve the existing UTC daily-summary contract. The public UI sends
		// Asia/Shanghai explicitly; half-hour queries retain their own default.
		zone = "UTC"
		if strings.HasSuffix(r.URL.Path, "/time-slots") {
			zone = "Asia/Shanghai"
		}
	}
	if _, e = time.LoadLocation(zone); e != nil {
		fail(w, 400, "VALIDATION_ERROR", "无效时区")
		return
	}
	if !from.Before(to) || to.Sub(from) > 366*24*time.Hour {
		fail(w, 400, "VALIDATION_ERROR", "查询范围必须在 366 天以内")
		return
	}
	kind := "drop"
	if strings.Contains(r.URL.Path, "/research") {
		kind = "research"
	}
	commissionStatus := -1
	if q.Get("status") != "" {
		commissionStatus, e = strconv.Atoi(q.Get("status"))
		if e != nil || commissionStatus < 0 || commissionStatus > 1 {
			fail(w, 400, "VALIDATION_ERROR", "无效委托状态")
			return
		}
	}
	if len(q.Get("comm")) > 128 {
		fail(w, 400, "VALIDATION_ERROR", "无效委托标识")
		return
	}
	args := []any{kind, from, to, q.Get("server"), q.Get("scene"), series, q.Get("project"), focus, q.Get("comm"), commissionStatus}
	// Keep discovery independent of the chosen dimension. Project filtering must
	// not change the denominator, and different series/focuses never share one.
	filtered := `WITH scope AS (
 SELECT *,COALESCE(comm,'') AS comm_key,COALESCE(commission_status,-1) AS status_key FROM stat_effective_events WHERE kind=$1 AND occurred_at >=$2 AND occurred_at<$3 AND ($4='' OR server=$4)
), base AS (
 SELECT * FROM scope WHERE ($5='' OR scene=$5) AND ($6::int=0 OR series=$6) AND ($8::int=-1 OR focus_series=$8) AND ($9='' OR comm_key=$9) AND ($10::int=-1 OR status_key=$10)
), filtered AS (SELECT * FROM base WHERE ($7='' OR project=$7)) `
	rowLimit := fmt.Sprintf(" LIMIT %d", maxSummaryRows+1)
	tx, e := s.Pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		internal(w, e)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	if strings.HasSuffix(r.URL.Path, "/options") {
		rows, err := tx.Query(r.Context(), filtered+`SELECT series,focus_series,scene,count(*) AS events,count(*) FILTER(WHERE event_type='refresh') AS appearances,count(*) FILTER(WHERE event_type<>'refresh') AS samples FROM scope GROUP BY series,focus_series,scene ORDER BY series DESC,focus_series DESC,scene`+rowLimit, args...)
		if err != nil {
			internal(w, err)
			return
		}
		options, err := collect(rows)
		if err != nil {
			internal(w, err)
			return
		}
		if len(options) > maxSummaryRows {
			fail(w, 400, "QUERY_TOO_BROAD", "请缩小筛选范围")
			return
		}
		if err = tx.Commit(r.Context()); err != nil {
			internal(w, err)
			return
		}
		output(w, 200, map[string]any{"schema_version": 2, "from": from, "to": to, "options": options})
		return
	}
	if strings.HasSuffix(r.URL.Path, "/time-slots") {
		rows, err := tx.Query(r.Context(), filtered+`,bucketed AS (
 SELECT *, (extract(hour FROM occurred_at AT TIME ZONE $11)*2+floor(extract(minute FROM occurred_at AT TIME ZONE $11)/30))::int AS slot FROM base
), totals AS (
 SELECT slot,series,focus_series,count(*) FILTER(WHERE event_type='refresh') AS appearances FROM bucketed GROUP BY slot,series,focus_series
)
SELECT b.slot AS slot_index,to_char(make_time(b.slot/2,(b.slot%2)*30,0),'HH24:MI') AS slot_start,
 b.series,b.focus_series,b.project,b.scene,b.comm_key AS comm,NULLIF(b.status_key,-1) AS status,
 count(*) FILTER(WHERE b.event_type='refresh') AS appearances,count(*) FILTER(WHERE b.event_type<>'refresh') AS samples,
 t.appearances AS total_appearances,COALESCE((count(*) FILTER(WHERE b.event_type='refresh'))::float8/NULLIF(t.appearances,0),0) AS appearance_rate
FROM bucketed b JOIN totals t USING(slot,series,focus_series) WHERE ($7='' OR b.project=$7)
GROUP BY b.slot,b.series,b.focus_series,b.project,b.scene,b.comm_key,b.status_key,t.appearances ORDER BY b.slot,b.series,b.focus_series,b.project,b.scene,b.comm_key,b.status_key`+rowLimit, append(args, zone)...)
		if err != nil {
			internal(w, err)
			return
		}
		slots, err := collect(rows)
		if err != nil {
			internal(w, err)
			return
		}
		if len(slots) > maxSummaryRows {
			fail(w, 400, "QUERY_TOO_BROAD", "请缩小时段筛选范围")
			return
		}
		if err = tx.Commit(r.Context()); err != nil {
			internal(w, err)
			return
		}
		output(w, 200, map[string]any{"schema_version": 2, "from": from, "to": to, "timezone": zone, "interval_minutes": 30, "slots": slots})
		return
	}
	rows, e := tx.Query(r.Context(), filtered+`, totals AS (
 SELECT series,focus_series,count(*) FILTER(WHERE event_type='refresh') AS appearances FROM base GROUP BY series,focus_series
)
SELECT e.series,e.focus_series,e.project,e.scene,e.comm_key AS comm,NULLIF(e.status_key,-1) AS status,count(*) FILTER(WHERE e.event_type='refresh') AS appearances,
 count(*) FILTER(WHERE e.event_type<>'refresh') AS samples,
 COALESCE(sum(e.duration_seconds) FILTER(WHERE e.event_type<>'refresh'),0)::bigint AS duration_seconds,
 t.appearances AS total_appearances,COALESCE((count(*) FILTER(WHERE e.event_type='refresh'))::float8/NULLIF(t.appearances,0),0) AS appearance_rate
FROM filtered e JOIN totals t USING(series,focus_series)
GROUP BY e.series,e.focus_series,e.project,e.scene,e.comm_key,e.status_key,t.appearances ORDER BY e.series,e.focus_series,e.project,e.scene,e.comm_key,e.status_key`+rowLimit, args...)
	if e != nil {
		internal(w, e)
		return
	}
	groups, e := collect(rows)
	if e != nil {
		internal(w, e)
		return
	}
	if len(groups) > maxSummaryRows {
		fail(w, 400, "QUERY_TOO_BROAD", "请缩小筛选范围")
		return
	}
	rows, e = tx.Query(r.Context(), filtered+`, denominators AS (
 SELECT series,focus_series,project,scene,comm_key,status_key,count(*) AS samples,sum(duration_seconds) AS seconds,
 count(*) FILTER(WHERE duration_seconds>0) AS timed_samples FROM filtered WHERE event_type<>'refresh' GROUP BY series,focus_series,project,scene,comm_key,status_key
), reward_rows AS MATERIALIZED (
 SELECT e.*,i.item,i.tag,i.amount FROM filtered e JOIN stat_effective_items i ON i.sha256=e.sha256 AND i.ordinal=e.ordinal
 WHERE e.event_type<>'refresh' AND i.item<>'PlaceHolder'
)
SELECT e.series,e.focus_series,e.project,e.scene,e.comm_key AS comm,NULLIF(e.status_key,-1) AS status,e.item,e.tag,count(*) AS occurrences,sum(e.amount)::bigint AS amount,
 d.samples,d.timed_samples,round(sum(e.amount)::numeric/d.samples,6)::float8 AS average_per_sample,
 round(count(*)::numeric/d.samples,6)::float8 AS drop_rate,
 CASE WHEN d.seconds>0 THEN round(COALESCE(sum(e.amount) FILTER(WHERE e.duration_seconds>0),0)::numeric*3600/d.seconds,6)::float8 ELSE NULL END AS amount_per_hour
FROM reward_rows e JOIN denominators d USING(series,focus_series,project,scene,comm_key,status_key)
WHERE e.status_key<0 OR e.scene NOT LIKE 'commission%' OR d.samples>50
GROUP BY e.series,e.focus_series,e.project,e.scene,e.comm_key,e.status_key,e.item,e.tag,d.samples,d.seconds,d.timed_samples
ORDER BY e.series,e.focus_series,e.project,e.scene,e.comm_key,e.status_key,e.item,e.tag`+rowLimit, args...)
	if e != nil {
		internal(w, e)
		return
	}
	items, e := collect(rows)
	if e != nil {
		internal(w, e)
		return
	}
	if len(items) > maxSummaryRows {
		fail(w, 400, "QUERY_TOO_BROAD", "请缩小筛选范围")
		return
	}
	rows, e = tx.Query(r.Context(), filtered+`SELECT (occurred_at AT TIME ZONE $11)::date::text AS day,count(*) FILTER(WHERE event_type<>'refresh') AS samples,count(*) FILTER(WHERE event_type='refresh') AS appearances FROM filtered GROUP BY day ORDER BY day`, append(args, zone)...)
	if e != nil {
		internal(w, e)
		return
	}
	daily, e := collect(rows)
	if e != nil {
		internal(w, e)
		return
	}
	if e = tx.Commit(r.Context()); e != nil {
		internal(w, e)
		return
	}
	output(w, 200, map[string]any{"schema_version": 2, "from": from, "to": to, "timezone": zone, "groups": groups, "items": items, "daily": daily})
}

// A candidate remains actionable only while a current result still reports it
// missing. Sources received just before their first result also remain visible.
// This is derived from the latest result, so rollback can reopen a candidate.
const candidateReviewCTE = `WITH candidate_review AS (
 SELECT c.*, (SELECT count(DISTINCT cs.sample_sha256) FROM candidate_sources cs
 LEFT JOIN stat_effective_results r ON r.sha256=cs.sample_sha256
 WHERE cs.candidate_id=c.id AND (r.sha256 IS NULL OR
 (r.status='needs_review' AND (r.payload->'candidate_ids') ? c.id))) AS blocked_samples
 FROM template_candidates c
), candidates AS (
 SELECT id,scene,image,metadata,icon_sha256,first_seen,last_seen,blocked_samples,
 (SELECT k.canonical_key FROM unknown_hashes h LEFT JOIN unknown_hash_overrides o ON o.local_key=h.local_key JOIN unknown_canonical_keys k ON k.key=COALESCE(o.cluster_key,h.cluster_key) WHERE h.local_key=candidate_review.icon_sha256) AS cluster_key,
 CASE WHEN status='pending' AND blocked_samples=0 THEN 'resolved' ELSE status END AS status
 FROM candidate_review
) `

const missingTemplateSQL = `(CASE WHEN jsonb_typeof(r.payload->'candidate_ids')='array' THEN jsonb_array_length(r.payload->'candidate_ids') ELSE 0 END>0 OR EXISTS(SELECT 1 FROM jsonb_array_elements_text(CASE WHEN jsonb_typeof(r.payload->'diagnostics')='array' THEN r.payload->'diagnostics' ELSE '[]'::jsonb END) d WHERE d='missing_template' OR d LIKE 'missing_template:%' OR d LIKE 'unmapped_cluster:%'))`

func (s *Service) adminList(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if strings.HasSuffix(path, "overview") {
		counts := map[string]int64{}
		images := map[string]int64{}
		for _, name := range []string{"complete", "needs_review", "invalid"} {
			var n, files int64
			if e := s.Pool.QueryRow(r.Context(), `SELECT count(*),COALESCE(sum(jsonb_array_length(payload->'source'->'files')),0)::bigint FROM stat_effective_results WHERE status=$1`, name).Scan(&n, &files); e != nil {
				internal(w, e)
				return
			}
			counts[name] = n
			images[name] = files
		}
		var pending int64
		if e := s.Pool.QueryRow(r.Context(), candidateReviewCTE+`SELECT count(*) FROM candidates WHERE status='pending'`).Scan(&pending); e != nil {
			internal(w, e)
			return
		}
		var templateReports, parserReports, templateImages, parserImages int64
		if e := s.Pool.QueryRow(r.Context(), `SELECT count(*) FILTER(WHERE `+missingTemplateSQL+`), count(*) FILTER(WHERE NOT `+missingTemplateSQL+`), COALESCE(sum(jsonb_array_length(payload->'source'->'files')) FILTER(WHERE `+missingTemplateSQL+`),0)::bigint, COALESCE(sum(jsonb_array_length(payload->'source'->'files')) FILTER(WHERE NOT `+missingTemplateSQL+`),0)::bigint FROM stat_effective_results r WHERE status='needs_review'`).Scan(&templateReports, &parserReports, &templateImages, &parserImages); e != nil {
			internal(w, e)
			return
		}
		rows, e := s.Pool.Query(r.Context(), `SELECT id,payload,updated_at,updated_at>now()-interval '90 seconds' AS online FROM stat_workers ORDER BY id`)
		if e != nil {
			internal(w, e)
			return
		}
		workers, e := collect(rows)
		if e != nil {
			internal(w, e)
			return
		}
		output(w, 200, map[string]any{"counts": counts, "image_counts": images, "pending_candidates": pending, "workers": workers, "issues": map[string]any{"missing_template": map[string]int64{"reports": templateReports, "images": templateImages}, "parser_failure": map[string]int64{"reports": parserReports, "images": parserImages}}})
		return
	}
	queries := map[string]string{
		"results":             `SELECT sha256,revision,status,payload,updated_at FROM stat_effective_results r WHERE ($1='' OR status=$1) AND ($2='' OR ($2='missing_template' AND ` + missingTemplateSQL + `) OR ($2='parser_failure' AND NOT ` + missingTemplateSQL + `)) ORDER BY updated_at DESC LIMIT 100`,
		"template-candidates": candidateReviewCTE + `SELECT c.*, (SELECT count(*) FROM candidate_sources cs WHERE cs.candidate_id=c.id) AS occurrences, (SELECT jsonb_agg(metadata||jsonb_build_object('sample_sha256',sample_sha256,'occurrence',occurrence)) FROM (SELECT * FROM candidate_sources WHERE candidate_id=c.id LIMIT 20) x) AS sources FROM candidates c WHERE ($1='' OR status=$1) ORDER BY last_seen DESC LIMIT 100`,
		"templates":           `SELECT id,item_id,name,category,rarity,series,scene,enabled,image FROM stat_templates WHERE ($1='' OR item_id=$1) ORDER BY item_id,id LIMIT 1000`,
		"template-releases":   `SELECT id,sha256,created_at,id=(SELECT release_id FROM template_active WHERE id) AS active FROM template_releases WHERE ($1='') ORDER BY id DESC LIMIT 30`,
		"worker-commands":     `SELECT * FROM worker_commands WHERE ($1='' OR state=$1) ORDER BY id DESC LIMIT 30`,
	}
	name := path[strings.LastIndex(path, "/")+1:]
	query, ok := queries[name]
	if !ok {
		fail(w, 404, "NOT_FOUND", "未知查询")
		return
	}
	status := r.URL.Query().Get("status")
	if name == "templates" {
		status = r.URL.Query().Get("item_id")
	}
	if name == "template-releases" {
		status = ""
	}
	args := []any{status}
	if name == "results" {
		reason := r.URL.Query().Get("reason")
		if reason != "" && reason != "missing_template" && reason != "parser_failure" {
			fail(w, 400, "VALIDATION_ERROR", "未知失败类型")
			return
		}
		args = append(args, reason)
	}
	rows, e := s.Pool.Query(r.Context(), query, args...)
	if e != nil {
		internal(w, e)
		return
	}
	data, e := collect(rows)
	if e != nil {
		internal(w, e)
		return
	}
	output(w, 200, map[string]any{"items": data})
}

func commandPayload(sources []json.RawMessage, templates []Template) []byte {
	b, _ := json.Marshal(map[string]any{"sources": sources, "templates": templates})
	return b
}
func validateTemplate(t Template) error {
	if !lootItemRE.MatchString(t.ItemID) || clusterRE.MatchString(t.ItemID) || t.ItemID == "PlaceHolder" || t.Name == "" || len(t.Name) > 128 || t.Series < 0 || t.Series > 100 || t.Rarity < 0 || t.Rarity > 6 {
		return fmt.Errorf("物品标识、名称、期数或稀有度无效")
	}
	switch t.Category {
	case "ship_blueprint", "equipment_blueprint", "consumable", "other":
	default:
		return fmt.Errorf("无效物品分类")
	}
	if t.Scene != "research" && t.Scene != "drop" && t.Scene != "all" {
		return fmt.Errorf("无效模板场景")
	}
	return nil
}
