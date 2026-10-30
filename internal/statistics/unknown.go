package statistics

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var clusterRE = regexp.MustCompile(`^U-[1-9][0-9]{0,18}$`)
var phashRE = regexp.MustCompile(`^[0-9a-f]{16}$`)
var variantRE = regexp.MustCompile(`_[0-9]+$`)

type iconEntry struct {
	Kind  string `json:"kind"`
	Item  string `json:"item,omitempty"`
	Key   string `json:"key,omitempty"`
	Image string `json:"image"`
}
type iconMatch struct {
	Match     string  `json:"match"`
	Item      string  `json:"item,omitempty"`
	Key       string  `json:"key,omitempty"`
	Score     float64 `json:"score"`
	PHASH     string  `json:"phash"`
	Hash      string  `json:"icon_sha256"`
	Stage     string  `json:"stage"`
	Suspected string  `json:"suspected_item,omitempty"`
}

func normalizedIcon(encoded string) (string, string, error) {
	raw, e := base64.StdEncoding.DecodeString(encoded)
	if e != nil {
		return "", "", e
	}
	cfg, e := png.DecodeConfig(bytes.NewReader(raw))
	if e != nil || cfg.Width != cfg.Height || (cfg.Width != 49 && cfg.Width != 96) {
		return "", "", errors.New("unknown icon must be 49x49 or a 96x96 item cell")
	}
	decoded, e := png.Decode(bytes.NewReader(raw))
	if e != nil {
		return "", "", e
	}
	offset := image.Point{}
	if cfg.Width == 96 {
		offset = image.Pt(40, 21)
	}
	icon := image.NewNRGBA(image.Rect(0, 0, 49, 49))
	pixels := make([]byte, 0, 49*49*3)
	for y := 0; y < 49; y++ {
		for x := 0; x < 49; x++ {
			r, g, b, a := decoded.At(x+offset.X, y+offset.Y).RGBA()
			if a != 65535 {
				return "", "", errors.New("unknown icons must be opaque RGB")
			}
			rr, gg, bb := uint8(r>>8), uint8(g>>8), uint8(b>>8)
			icon.SetNRGBA(x, y, color.NRGBA{R: rr, G: gg, B: bb, A: 255})
			pixels = append(pixels, rr, gg, bb)
		}
	}
	sum := sha256.Sum256(pixels)
	var out bytes.Buffer
	if e = png.Encode(&out, icon); e != nil {
		return "", "", e
	}
	return hex.EncodeToString(sum[:]), base64.StdEncoding.EncodeToString(out.Bytes()), nil
}

func (s *Service) matchUnknown(ctx context.Context, encoded string, entries []iconEntry, revision int64) (iconMatch, error) {
	parameters := defaultMatchThresholds()
	if revision > 0 {
		var payload []byte
		if e := s.Pool.QueryRow(ctx, `SELECT payload FROM template_releases WHERE id=$1`, revision).Scan(&payload); e != nil {
			return iconMatch{}, e
		}
		if len(payload) > 0 && payload[0] == '{' {
			var bundle contentBundle
			if e := json.Unmarshal(payload, &bundle); e != nil {
				return iconMatch{}, e
			}
			parameters = bundle.Thresholds
		}
	}
	body, e := json.Marshal(map[string]any{"icons": []string{encoded}, "entries": entries, "revision": revision, "include_base": true, "thresholds": parameters})
	if e != nil {
		return iconMatch{}, e
	}
	request, e := s.workerRequest(ctx, "/internal/v1/icon-matches", bytes.NewReader(body))
	if e != nil {
		return iconMatch{}, e
	}
	response, e := workerClient(12 * time.Second).Do(request)
	if e != nil {
		return iconMatch{}, e
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != 200 {
		return iconMatch{}, fmt.Errorf("icon matcher HTTP %d", response.StatusCode)
	}
	var result struct {
		Results []iconMatch `json:"results"`
	}
	if e = json.NewDecoder(io.LimitReader(response.Body, 256<<10)).Decode(&result); e != nil {
		return iconMatch{}, e
	}
	if len(result.Results) != 1 {
		return iconMatch{}, errors.New("invalid icon matcher result count")
	}
	match := result.Results[0]
	if !hashRE.MatchString(match.Hash) || !phashRE.MatchString(match.PHASH) || math.IsNaN(match.Score) || math.IsInf(match.Score, 0) || match.Score < -1 || match.Score > 1.00001 {
		return iconMatch{}, errors.New("invalid icon match fingerprint/score")
	}
	switch match.Match {
	case "formal":
		if !lootItemRE.MatchString(match.Item) || clusterRE.MatchString(match.Item) {
			return iconMatch{}, errors.New("invalid formal item")
		}
		match.Item = variantRE.ReplaceAllString(match.Item, "")
	case "cluster":
		if !clusterRE.MatchString(match.Key) {
			return iconMatch{}, errors.New("invalid cluster identity")
		}
	case "new":
	default:
		return iconMatch{}, errors.New("invalid icon decision")
	}
	return match, nil
}

func (s *Service) unknownSnapshot(ctx context.Context) (int64, int64, []iconEntry, error) {
	tx, e := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return 0, 0, nil, e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var generation, revision int64
	e = tx.QueryRow(ctx, `SELECT generation,COALESCE((SELECT release_id FROM template_active WHERE id),0) FROM unknown_state WHERE id`).Scan(&generation, &revision)
	if e != nil {
		return 0, 0, nil, e
	}
	entries := []iconEntry{}
	if revision > 0 {
		var raw []byte
		if e = tx.QueryRow(ctx, `SELECT payload FROM template_releases WHERE id=$1`, revision).Scan(&raw); e != nil {
			return 0, 0, nil, e
		}
		ts, e := decodeTemplatePayload(raw)
		if e != nil {
			return 0, 0, nil, e
		}
		for _, t := range ts {
			if t.Enabled && t.Kind != "temporary" {
				entries = append(entries, iconEntry{Kind: "formal", Item: t.ItemID, Image: t.Image})
			}
		}
	}
	rows, e := tx.Query(ctx, `SELECT c.key,i.image FROM unknown_clusters c JOIN unknown_cluster_exemplars x ON x.cluster_key=c.key JOIN unknown_icons i ON i.icon_sha256=x.icon_sha256 WHERE c.status='pending' AND NOT EXISTS(SELECT 1 FROM unknown_hash_overrides o JOIN unknown_canonical_keys k ON k.key=o.cluster_key WHERE o.local_key=x.icon_sha256 AND k.canonical_key<>c.key) ORDER BY c.id,x.icon_sha256 LIMIT 10001`)
	if e != nil {
		return 0, 0, nil, e
	}
	for rows.Next() {
		var entry iconEntry
		entry.Kind = "cluster"
		if e = rows.Scan(&entry.Key, &entry.Image); e != nil {
			rows.Close()
			return 0, 0, nil, e
		}
		entries = append(entries, entry)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return 0, 0, nil, e
	}
	if len(entries) > 10000 {
		return 0, 0, nil, errors.New("unknown matching snapshot exceeds 10000 entries")
	}
	return generation, revision, entries, tx.Commit(ctx)
}

type unknownReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func lookupUnknown(ctx context.Context, reader unknownReader, hash string) (*string, *string, error) {
	var item, key *string
	e := reader.QueryRow(ctx, `SELECT item,canonical_key FROM unknown_effective_hashes WHERE local_key=$1 AND is_current`, hash).Scan(&item, &key)
	return item, key, e
}

func saveCandidate(ctx context.Context, tx pgx.Tx, c Candidate, hash string) error {
	metadata, e := json.Marshal(c.Metadata)
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO template_candidates(id,scene,image,metadata,icon_sha256) VALUES($1,$2,$3,$4,$5) ON CONFLICT(id) DO UPDATE SET last_seen=now(),icon_sha256=EXCLUDED.icon_sha256`, c.ID, c.Scene, c.Image, metadata, hash); e != nil {
		return e
	}
	result, e := tx.Exec(ctx, `INSERT INTO candidate_sources(candidate_id,sample_sha256,occurrence,metadata) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, c.ID, c.Sample, c.Occurrence, metadata)
	if e != nil {
		return e
	}
	if result.RowsAffected() > 0 {
		_, e = tx.Exec(ctx, `UPDATE unknown_clusters SET hits=hits+1,last_seen=now() WHERE key=(SELECT k.canonical_key FROM unknown_hashes h LEFT JOIN unknown_hash_overrides o ON o.local_key=h.local_key JOIN unknown_canonical_keys k ON k.key=COALESCE(o.cluster_key,h.cluster_key) WHERE h.local_key=$1)`, hash)
	}
	return e
}

func (s *Service) candidateReceipt(ctx context.Context, c Candidate, hash string, created bool) (map[string]any, error) {
	tx, e := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	item, key, e := lookupUnknown(ctx, tx, hash)
	if e != nil {
		return nil, e
	}
	var revision int64
	if e = tx.QueryRow(ctx, `SELECT COALESCE(release_id,0) FROM template_active WHERE id`).Scan(&revision); e != nil {
		return nil, e
	}
	result := map[string]any{"id": c.ID, "accepted": true, "local_key": hash, "revision": revision, "need_screenshot": false}
	if item != nil {
		result["match"], result["item"] = "formal", *item
		return result, tx.Commit(ctx)
	}
	if key == nil {
		return nil, errors.New("unknown identity is missing")
	}
	result["match"], result["key"] = "cluster", *key
	if created {
		result["match"] = "new"
	}
	var image string
	var status string
	var count int
	if e = tx.QueryRow(ctx, `SELECT i.image,c.status,(SELECT count(*) FROM unknown_screenshots p WHERE p.cluster_key=c.key AND p.expires_at>now()) FROM unknown_clusters c JOIN unknown_icons i ON i.icon_sha256=c.representative_sha256 WHERE c.key=$1`, *key).Scan(&image, &status, &count); e != nil {
		return nil, e
	}
	result["representative"], result["need_screenshot"] = image, count < 3 && status == "pending"
	return result, tx.Commit(ctx)
}

func (s *Service) clusteredCandidate(w http.ResponseWriter, r *http.Request, c Candidate) {
	hash, encoded, e := normalizedIcon(c.Image)
	if e != nil {
		fail(w, 400, "VALIDATION_ERROR", e.Error())
		return
	}
	if given, ok := c.Metadata["icon_sha256"].(string); ok && given != hash {
		fail(w, 400, "VALIDATION_ERROR", "图标像素哈希不一致")
		return
	}
	// L0 is independent of scene. Only unknown fingerprints enter the bounded
	// native-matcher gate; repeat uploads recheck L0 after acquiring that gate.
	for attempt := 0; attempt < 16; attempt++ {
		_, _, known := lookupUnknown(r.Context(), s.Pool, hash)
		var decision iconMatch
		var generation, revision int64
		if errors.Is(known, pgx.ErrNoRows) {
			select {
			case s.matchSlots <- struct{}{}:
			case <-r.Context().Done():
				internal(w, r.Context().Err())
				return
			}
			_, _, known = lookupUnknown(r.Context(), s.Pool, hash)
			if errors.Is(known, pgx.ErrNoRows) {
				var entries []iconEntry
				generation, revision, entries, e = s.unknownSnapshot(r.Context())
				if e == nil {
					decision, e = s.matchUnknown(r.Context(), encoded, entries, revision)
				}
			}
			<-s.matchSlots
			if e != nil {
				internal(w, e)
				return
			}
		} else if known != nil {
			internal(w, known)
			return
		}
		tx, e := s.Pool.Begin(r.Context())
		if e != nil {
			internal(w, e)
			return
		}
		if errors.Is(known, pgx.ErrNoRows) {
			if _, e = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtext('unknown-icons'))`); e != nil {
				_ = tx.Rollback(r.Context())
				internal(w, e)
				return
			}
		}
		_, _, current := lookupUnknown(r.Context(), tx, hash)
		created := false
		if errors.Is(current, pgx.ErrNoRows) {
			var nowGeneration, nowRevision int64
			e = tx.QueryRow(r.Context(), `SELECT generation,COALESCE((SELECT release_id FROM template_active WHERE id),0) FROM unknown_state WHERE id`).Scan(&nowGeneration, &nowRevision)
			if e != nil {
				_ = tx.Rollback(r.Context())
				internal(w, e)
				return
			}
			if nowGeneration != generation || nowRevision != revision {
				_ = tx.Rollback(r.Context())
				continue
			}
			if decision.Hash != hash {
				_ = tx.Rollback(r.Context())
				fail(w, 502, "INVALID_MATCHER", "图标匹配身份不一致")
				return
			}
			if decision.Suspected != "" {
				if c.Metadata == nil {
					c.Metadata = map[string]any{}
				}
				c.Metadata["suspected_item"] = decision.Suspected
				c.Metadata["match_score"] = decision.Score
			}
			if _, e = tx.Exec(r.Context(), `INSERT INTO unknown_icons(icon_sha256,image,phash) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, hash, encoded, decision.PHASH); e != nil {
				_ = tx.Rollback(r.Context())
				internal(w, e)
				return
			}
			var key *string
			var item *string
			if decision.Match == "formal" {
				item = &decision.Item
			} else {
				if decision.Match == "cluster" {
					key = &decision.Key
				} else {
					var value string
					e = tx.QueryRow(r.Context(), `INSERT INTO unknown_clusters(representative_sha256) VALUES($1) RETURNING key`, hash).Scan(&value)
					if e != nil {
						_ = tx.Rollback(r.Context())
						internal(w, e)
						return
					}
					key = &value
					created = true
				}
			}
			if _, e = tx.Exec(r.Context(), `INSERT INTO unknown_hashes(local_key,cluster_key,formal_item,formal_revision) VALUES($1,$2,$3,$4) ON CONFLICT(local_key) DO UPDATE SET cluster_key=EXCLUDED.cluster_key,formal_item=EXCLUDED.formal_item,formal_revision=EXCLUDED.formal_revision`, hash, key, item, revision); e != nil {
				_ = tx.Rollback(r.Context())
				internal(w, e)
				return
			}
			if key != nil {
				_, e = tx.Exec(r.Context(), `INSERT INTO unknown_cluster_exemplars(cluster_key,icon_sha256) SELECT $1,$2 WHERE (SELECT count(*) FROM unknown_cluster_exemplars WHERE cluster_key=$1)<9 ON CONFLICT DO NOTHING`, *key, hash)
				if e != nil {
					_ = tx.Rollback(r.Context())
					internal(w, e)
					return
				}
			}
			if _, e = tx.Exec(r.Context(), `UPDATE unknown_state SET generation=generation+1 WHERE id`); e != nil {
				_ = tx.Rollback(r.Context())
				internal(w, e)
				return
			}
		} else if current != nil {
			_ = tx.Rollback(r.Context())
			internal(w, current)
			return
		}
		if e = saveCandidate(r.Context(), tx, c, hash); e != nil {
			_ = tx.Rollback(r.Context())
			internal(w, e)
			return
		}
		if e = tx.Commit(r.Context()); e != nil {
			internal(w, e)
			return
		}
		receipt, e := s.candidateReceipt(r.Context(), c, hash, created)
		if e != nil {
			internal(w, e)
			return
		}
		output(w, 200, receipt)
		return
	}
	fail(w, 503, "MATCH_SNAPSHOT_BUSY", "匹配快照正在更新，请重试")
}

func (s *Service) screenshotReference(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key       string `json:"key"`
		Sample    string `json:"sample_sha256"`
		Candidate string `json:"candidate_id"`
		File      int    `json:"file_index"`
		Frame     int    `json:"frame_index"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !clusterRE.MatchString(body.Key) || !hashRE.MatchString(body.Sample) || !hashRE.MatchString(body.Candidate) || body.File < 0 || body.File >= 1000 || body.Frame < 0 || body.Frame >= 10 {
		fail(w, 400, "VALIDATION_ERROR", "无效原图引用")
		return
	}
	tx, e := s.Pool.Begin(r.Context())
	if e != nil {
		internal(w, e)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	if _, e = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtext('unknown-icons'))`); e != nil {
		internal(w, e)
		return
	}
	var associated bool
	e = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM candidate_sources cs JOIN template_candidates c ON c.id=cs.candidate_id JOIN unknown_hashes h ON h.local_key=c.icon_sha256 LEFT JOIN unknown_hash_overrides o ON o.local_key=h.local_key JOIN unknown_canonical_keys k ON k.key=COALESCE(o.cluster_key,h.cluster_key) JOIN stat_results r ON r.sha256=cs.sample_sha256 WHERE cs.candidate_id=$1 AND cs.sample_sha256=$2 AND k.canonical_key=$3 AND (cs.metadata->>'file_index')::int=$4 AND (cs.metadata->>'frame_index')::int=$5 AND $4<jsonb_array_length(r.payload->'source'->'files'))`, body.Candidate, body.Sample, body.Key, body.File, body.Frame).Scan(&associated)
	if e != nil {
		internal(w, e)
		return
	}
	if !associated {
		fail(w, 400, "VALIDATION_ERROR", "原图与候选不关联")
		return
	}
	if _, e = tx.Exec(r.Context(), `DELETE FROM unknown_screenshots WHERE expires_at<=now()`); e != nil {
		internal(w, e)
		return
	}
	result, e := tx.Exec(r.Context(), `INSERT INTO unknown_screenshots(cluster_key,sample_sha256,file_index,frame_index) SELECT $1,$2,$3,$4 WHERE (SELECT count(*) FROM unknown_screenshots WHERE cluster_key=$1)<3 AND EXISTS(SELECT 1 FROM unknown_clusters WHERE key=$1 AND status='pending') ON CONFLICT DO NOTHING`, body.Key, body.Sample, body.File, body.Frame)
	if e != nil {
		internal(w, e)
		return
	}
	if e = tx.Commit(r.Context()); e != nil {
		internal(w, e)
		return
	}
	output(w, 200, map[string]any{"accepted": true, "stored": result.RowsAffected() > 0})
}

func stripVariant(item string) string { return variantRE.ReplaceAllString(strings.TrimSpace(item), "") }
