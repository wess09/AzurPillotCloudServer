package statistics

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *Service) Apply(ctx context.Context, r Result) (Receipt, error) {
	ack := Receipt{SHA256: r.SHA256, Revision: r.Revision}
	if e := r.validateLimits(s.FileBytes, s.GroupBytes); e != nil {
		ack.Status = "invalid"
		ack.Error = e.Error()
		return ack, nil
	}
	b, e := json.Marshal(r)
	if e != nil {
		return ack, e
	}
	sum := sha256.Sum256(b)
	digest := hex.EncodeToString(sum[:])
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return ack, e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, r.SHA256); e != nil {
		return ack, e
	}
	var oldHash string
	e = tx.QueryRow(ctx, `SELECT content_hash FROM stat_result_versions WHERE sha256=$1 AND revision=$2`, r.SHA256, r.Revision).Scan(&oldHash)
	if e == nil {
		if oldHash == digest {
			ack.Status = "duplicate"
		} else {
			ack.Status = "conflict"
			ack.Error = "同一版本内容不同"
		}
		return ack, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return ack, e
	}
	var current int64
	e = tx.QueryRow(ctx, `SELECT revision FROM stat_results WHERE sha256=$1`, r.SHA256).Scan(&current)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return ack, e
	}
	if r.Revision != current+1 {
		ack.Status = "conflict"
		ack.Revision = current
		ack.Error = fmt.Sprintf("expected revision %d", current+1)
		return ack, nil
	}
	if _, e = tx.Exec(ctx, `INSERT INTO stat_results(sha256,revision,status,payload) VALUES($1,$2,$3,$4) ON CONFLICT(sha256) DO UPDATE SET revision=EXCLUDED.revision,status=EXCLUDED.status,payload=EXCLUDED.payload,updated_at=now()`, r.SHA256, r.Revision, r.Status, b); e != nil {
		return ack, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO stat_result_versions(sha256,revision,content_hash,payload) VALUES($1,$2,$3,$4)`, r.SHA256, r.Revision, digest, b); e != nil {
		return ack, e
	}
	if _, e = tx.Exec(ctx, `DELETE FROM stat_events WHERE sha256=$1`, r.SHA256); e != nil {
		return ack, e
	}
	if _, e = tx.Exec(ctx, `DELETE FROM stat_review_events WHERE sha256=$1`, r.SHA256); e != nil {
		return ack, e
	}
	if r.Status == "complete" || r.Status == "needs_review" {
		eventTable, itemTable := "stat_events", "stat_items"
		if r.Status == "needs_review" {
			eventTable, itemTable = "stat_review_events", "stat_review_items"
		}
		at, _ := time.Parse(time.RFC3339Nano, r.Source.Metadata.CollectedAt)
		for ordinal, event := range r.Events {
			if _, e = tx.Exec(ctx, `INSERT INTO `+eventTable+`(sha256,ordinal,kind,event_type,occurred_at,server,scene,series,project,focus_series,duration_seconds,record_id,comm,name,commission_status) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, r.SHA256, ordinal, r.Source.Metadata.Project, event.Type, at, r.Source.Metadata.Server, r.Source.Metadata.Scene, event.Series, event.Project, event.FocusSeries, event.Duration, event.RecordID, event.Comm, event.Name, event.Status); e != nil {
				return ack, e
			}
			for _, item := range event.Items {
				if _, e = tx.Exec(ctx, `INSERT INTO `+itemTable+`(sha256,ordinal,item,tag,amount) VALUES($1,$2,$3,$4,$5)`, r.SHA256, ordinal, item.Item, item.Tag, item.Amount); e != nil {
					return ack, e
				}
			}
			for _, item := range event.Unresolved {
				if _, e = tx.Exec(ctx, `INSERT INTO stat_review_items(sha256,ordinal,tag,amount,local_key,candidate_id) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(sha256,ordinal,item,tag,local_key,candidate_id) DO UPDATE SET amount=stat_review_items.amount+EXCLUDED.amount`, r.SHA256, ordinal, item.Tag, item.Amount, item.LocalKey, item.CandidateID); e != nil {
					return ack, e
				}
			}
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return ack, e
	}
	ack.Status = "accepted"
	return ack, nil
}
func (s *Service) ingest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Results []Result `json:"results"`
	}
	if !decode(w, r, &body) {
		return
	}
	if len(body.Results) == 0 || len(body.Results) > 100 {
		fail(w, 400, "VALIDATION_ERROR", "每次提交 1..100 份结果")
		return
	}
	acks := []Receipt{}
	for _, result := range body.Results {
		ack, e := s.Apply(r.Context(), result)
		if e != nil {
			ack = Receipt{SHA256: result.SHA256, Revision: result.Revision, Status: "retry", Error: "数据库暂不可用"}
		}
		acks = append(acks, ack)
	}
	output(w, 200, map[string]any{"schema_version": 2, "receipts": acks})
}
func (s *Service) receipt(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("sha256")
	if !hashRE.MatchString(id) {
		fail(w, 400, "VALIDATION_ERROR", "无效哈希")
		return
	}
	var version int64
	var status string
	e := s.Pool.QueryRow(r.Context(), `SELECT revision,status FROM stat_results WHERE sha256=$1`, id).Scan(&version, &status)
	if errors.Is(e, pgx.ErrNoRows) {
		fail(w, 404, "NOT_FOUND", "未收到该结果")
		return
	}
	if e != nil {
		internal(w, e)
		return
	}
	output(w, 200, map[string]any{"sha256": id, "revision": version, "status": status})
}
