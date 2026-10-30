package statistics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
)

func refreshClusterRepresentative(ctx context.Context, tx pgx.Tx, key string) (bool, error) {
	rows, e := tx.Query(ctx, `SELECT h.local_key FROM unknown_effective_hashes h LEFT JOIN unknown_cluster_exemplars x ON x.cluster_key=$1 AND x.icon_sha256=h.local_key WHERE h.canonical_key=$1 ORDER BY (h.local_key=(SELECT representative_sha256 FROM unknown_clusters WHERE key=$1)) DESC,(x.icon_sha256 IS NOT NULL) DESC,h.local_key LIMIT 9`, key)
	if e != nil {
		return false, e
	}
	hashes := []string{}
	for rows.Next() {
		var hash string
		if e = rows.Scan(&hash); e != nil {
			rows.Close()
			return false, e
		}
		hashes = append(hashes, hash)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return false, e
	}
	if _, e = tx.Exec(ctx, `DELETE FROM unknown_cluster_exemplars WHERE cluster_key=$1`, key); e != nil {
		return false, e
	}
	if len(hashes) == 0 {
		_, e = tx.Exec(ctx, `UPDATE unknown_clusters SET representative_sha256=NULL,status='empty' WHERE key=$1`, key)
		return true, e
	}
	if _, e = tx.Exec(ctx, `UPDATE unknown_clusters SET representative_sha256=$2,status=CASE WHEN status='empty' THEN 'pending' ELSE status END WHERE key=$1`, key, hashes[0]); e != nil {
		return false, e
	}
	for _, hash := range hashes {
		if _, e = tx.Exec(ctx, `INSERT INTO unknown_cluster_exemplars(cluster_key,icon_sha256) VALUES($1,$2)`, key, hash); e != nil {
			return false, e
		}
	}
	return false, nil
}

func repairClusterRepresentatives(ctx context.Context, tx pgx.Tx) error {
	rows, e := tx.Query(ctx, `SELECT c.key FROM unknown_clusters c JOIN unknown_canonical_keys k ON k.key=c.key WHERE k.canonical_key=c.key AND (c.representative_sha256 IS NULL OR NOT EXISTS(SELECT 1 FROM unknown_effective_hashes h WHERE h.local_key=c.representative_sha256 AND h.canonical_key=c.key)) ORDER BY c.id LIMIT 20001`)
	if e != nil {
		return e
	}
	keys := []string{}
	for rows.Next() {
		var key string
		if e = rows.Scan(&key); e != nil {
			rows.Close()
			return e
		}
		keys = append(keys, key)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if len(keys) > 20000 {
		return fmt.Errorf("too many cluster representatives to restore")
	}
	for _, key := range keys {
		if _, e = refreshClusterRepresentative(ctx, tx, key); e != nil {
			return e
		}
	}
	return nil
}

func moveScreenshotReferences(ctx context.Context, tx pgx.Tx, from, to string) error {
	_, e := tx.Exec(ctx, `INSERT INTO unknown_screenshots(cluster_key,sample_sha256,file_index,frame_index,expires_at) SELECT $1,sample_sha256,file_index,frame_index,expires_at FROM unknown_screenshots WHERE cluster_key=$2 AND expires_at>now() AND EXISTS(SELECT 1 FROM unknown_clusters WHERE key=$1 AND status='pending') LIMIT GREATEST(0,3-(SELECT count(*) FROM unknown_screenshots WHERE cluster_key=$1 AND expires_at>now())) ON CONFLICT DO NOTHING`, to, from)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `DELETE FROM unknown_screenshots WHERE cluster_key=$1`, from)
	return e
}

func canonicalCluster(ctx context.Context, tx pgx.Tx, key string) (string, error) {
	var canonical string
	e := tx.QueryRow(ctx, `SELECT canonical_key FROM unknown_canonical_keys WHERE key=$1`, key).Scan(&canonical)
	return canonical, e
}

func (s *Service) clusterEdit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		From string `json:"from_key"`
		To   string `json:"to_key"`
		Hash string `json:"local_key"`
	}
	if !decode(w, r, &body) {
		return
	}
	action := r.PathValue("action")
	if action != "split" && action != "merge" {
		fail(w, 404, "NOT_FOUND", "未知簇操作")
		return
	}
	split := action == "split"
	if !clusterRE.MatchString(body.From) || (body.To != "" && !clusterRE.MatchString(body.To)) || (!split && body.To == "") || (split && !hashRE.MatchString(body.Hash)) {
		fail(w, 400, "VALIDATION_ERROR", "无效簇操作")
		return
	}
	tx, e := s.Pool.Begin(r.Context())
	if e != nil {
		internal(w, e)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	if _, e = tx.Exec(r.Context(), `SELECT id FROM template_active WHERE id FOR UPDATE`); e != nil {
		internal(w, e)
		return
	}
	if _, e = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtext('unknown-icons'))`); e != nil {
		internal(w, e)
		return
	}
	from, e := canonicalCluster(r.Context(), tx, body.From)
	if errors.Is(e, pgx.ErrNoRows) {
		fail(w, 404, "NOT_FOUND", "来源簇不存在")
		return
	}
	if e != nil {
		internal(w, e)
		return
	}
	target := ""
	if body.To != "" {
		target, e = canonicalCluster(r.Context(), tx, body.To)
		if errors.Is(e, pgx.ErrNoRows) {
			fail(w, 404, "NOT_FOUND", "目标簇不存在")
			return
		}
		if e != nil {
			internal(w, e)
			return
		}
		var active bool
		if e = tx.QueryRow(r.Context(), `SELECT representative_sha256 IS NOT NULL FROM unknown_clusters WHERE key=$1`, target).Scan(&active); e != nil {
			internal(w, e)
			return
		}
		if !active {
			fail(w, 409, "EMPTY_CLUSTER", "目标簇已为空，请刷新后重试")
			return
		}
	}
	if split {
		_, current, e := lookupUnknown(r.Context(), tx, body.Hash)
		if errors.Is(e, pgx.ErrNoRows) {
			fail(w, 404, "NOT_FOUND", "图标不存在")
			return
		}
		if e != nil {
			internal(w, e)
			return
		}
		if current == nil || *current != from {
			fail(w, 409, "CLUSTER_CHANGED", "图标归属已变化，请刷新后重试")
			return
		}
		if target == from {
			fail(w, 400, "VALIDATION_ERROR", "拆分目标必须不同")
			return
		}
		if target == "" {
			e = tx.QueryRow(r.Context(), `INSERT INTO unknown_clusters(representative_sha256) VALUES($1) RETURNING key`, body.Hash).Scan(&target)
			if e != nil {
				internal(w, e)
				return
			}
		}
		if _, e = tx.Exec(r.Context(), `INSERT INTO unknown_hash_overrides(local_key,cluster_key) VALUES($1,$2) ON CONFLICT(local_key) DO UPDATE SET cluster_key=EXCLUDED.cluster_key`, body.Hash, target); e != nil {
			internal(w, e)
			return
		}
		empty, err := refreshClusterRepresentative(r.Context(), tx, from)
		if err != nil {
			internal(w, err)
			return
		}
		if empty {
			if e = moveScreenshotReferences(r.Context(), tx, from, target); e != nil {
				internal(w, e)
				return
			}
		}
		if _, e = tx.Exec(r.Context(), `INSERT INTO unknown_cluster_exemplars(cluster_key,icon_sha256) SELECT $1,$2 WHERE (SELECT count(*) FROM unknown_cluster_exemplars WHERE cluster_key=$1)<9 ON CONFLICT DO NOTHING`, target, body.Hash); e != nil {
			internal(w, e)
			return
		}
	} else {
		if from == target {
			output(w, 200, map[string]any{"saved": true, "key": target})
			return
		}
		// Conflicting human labels need explicit correction before identities merge.
		var fromItem, toItem *string
		e = tx.QueryRow(r.Context(), `SELECT (SELECT item FROM unknown_mapping_drafts WHERE cluster_key=$1),(SELECT item FROM unknown_mapping_drafts WHERE cluster_key=$2)`, from, target).Scan(&fromItem, &toItem)
		if e != nil {
			internal(w, e)
			return
		}
		if fromItem != nil && toItem != nil && *fromItem != *toItem {
			fail(w, 409, "MAPPING_CONFLICT", "两个簇的物品名称不同，请先修正分类")
			return
		}
		if fromItem != nil && toItem == nil {
			if _, e = tx.Exec(r.Context(), `INSERT INTO unknown_mapping_drafts(cluster_key,item) VALUES($1,$2)`, target, *fromItem); e != nil {
				internal(w, e)
				return
			}
		}
		if _, e = tx.Exec(r.Context(), `DELETE FROM unknown_mapping_drafts WHERE cluster_key=$1`, from); e != nil {
			internal(w, e)
			return
		}
		if _, e = tx.Exec(r.Context(), `UPDATE unknown_cluster_aliases SET to_key=$2 WHERE to_key=$1`, from, target); e != nil {
			internal(w, e)
			return
		}
		if _, e = tx.Exec(r.Context(), `INSERT INTO unknown_cluster_aliases(from_key,to_key) VALUES($1,$2) ON CONFLICT(from_key) DO UPDATE SET to_key=EXCLUDED.to_key`, from, target); e != nil {
			internal(w, e)
			return
		}
		if _, e = tx.Exec(r.Context(), `UPDATE unknown_clusters SET status='merged' WHERE key=$1`, from); e != nil {
			internal(w, e)
			return
		}
		if _, e = tx.Exec(r.Context(), `INSERT INTO unknown_cluster_exemplars(cluster_key,icon_sha256) SELECT $1,icon_sha256 FROM unknown_cluster_exemplars WHERE cluster_key=$2 AND icon_sha256 NOT IN(SELECT icon_sha256 FROM unknown_cluster_exemplars WHERE cluster_key=$1) ORDER BY icon_sha256 LIMIT GREATEST(0,9-(SELECT count(*) FROM unknown_cluster_exemplars WHERE cluster_key=$1)) ON CONFLICT DO NOTHING`, target, from); e != nil {
			internal(w, e)
			return
		}
		if e = moveScreenshotReferences(r.Context(), tx, from, target); e != nil {
			internal(w, e)
			return
		}
	}
	templates, e := readTemplates(r.Context(), tx)
	if e != nil {
		internal(w, e)
		return
	}
	payload, _ := json.Marshal(templates)
	revision, _, e := releaseInTx(r.Context(), tx, payload, s.signingKey)
	if e != nil {
		internal(w, e)
		return
	}
	if _, e = tx.Exec(r.Context(), `UPDATE unknown_cluster_aliases SET revision=$1 WHERE revision=0`, revision); e != nil {
		internal(w, e)
		return
	}
	if e = tx.Commit(r.Context()); e != nil {
		internal(w, e)
		return
	}
	output(w, 200, map[string]any{"saved": true, "key": target, "release_id": revision})
}

func (s *Service) clusters(w http.ResponseWriter, r *http.Request) {
	rows, e := s.Pool.Query(r.Context(), `SELECT c.key,c.status,c.hits,c.first_seen,c.last_seen,i.image,
 (SELECT jsonb_agg(jsonb_build_object('icon_sha256',x.icon_sha256,'image',n.image)) FROM unknown_cluster_exemplars x JOIN unknown_icons n USING(icon_sha256) WHERE x.cluster_key=c.key) AS exemplars,
 (SELECT count(*) FROM unknown_hashes h LEFT JOIN unknown_hash_overrides o ON o.local_key=h.local_key JOIN unknown_canonical_keys k ON k.key=COALESCE(o.cluster_key,h.cluster_key) WHERE k.canonical_key=c.key) AS icons
 FROM unknown_clusters c JOIN unknown_icons i ON i.icon_sha256=c.representative_sha256 ORDER BY c.last_seen DESC LIMIT 100`)
	if e != nil {
		internal(w, e)
		return
	}
	result, e := collect(rows)
	if e != nil {
		internal(w, e)
		return
	}
	output(w, 200, map[string]any{"items": result})
}
