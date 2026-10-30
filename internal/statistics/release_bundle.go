package statistics

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type contentBundle struct {
	Schema        int               `json:"schema_version"`
	Revision      int64             `json:"revision"`
	Templates     []Template        `json:"templates"`
	Mappings      map[string]string `json:"mappings"`
	Aliases       map[string]string `json:"aliases"`
	HashOverrides map[string]string `json:"hash_overrides"`
	Thresholds    map[string]any    `json:"match_thresholds"`
}

func defaultMatchThresholds() map[string]any {
	return map[string]any{"formal": 0.92, "cluster": 0.96, "margin": 0.03, "suspected": 0.85, "hamming": 6, "color": 30, "low_variance": 5, "temporary": 0.95}
}
func validateMatchThresholds(input map[string]any) (map[string]any, error) {
	result := defaultMatchThresholds()
	for key, value := range input {
		if _, ok := result[key]; !ok {
			return nil, errors.New("unknown match threshold")
		}
		switch number := value.(type) {
		case float64:
			if math.IsNaN(number) || math.IsInf(number, 0) {
				return nil, errors.New("invalid match threshold")
			}
		case int:
		default:
			return nil, errors.New("invalid match threshold type")
		}
		result[key] = value
	}
	number := func(key string) float64 {
		switch value := result[key].(type) {
		case int:
			return float64(value)
		case float64:
			return value
		}
		return math.NaN()
	}
	if !(number("suspected") >= 0 && number("suspected") <= number("formal") && number("formal") <= number("cluster") && number("cluster") <= 1 && number("margin") >= 0 && number("margin") <= 1 && number("temporary") >= 0 && number("temporary") <= 1 && number("hamming") >= 0 && number("hamming") <= 64 && math.Trunc(number("hamming")) == number("hamming") && number("color") >= 0 && number("color") <= 255 && number("low_variance") >= 0 && number("low_variance") <= 255) {
		return nil, errors.New("invalid match threshold range")
	}
	return result, nil
}
func decodeTemplatePayload(payload []byte) ([]Template, error) {
	var templates []Template
	if len(payload) > 0 && payload[0] == '[' {
		e := json.Unmarshal(payload, &templates)
		return templates, e
	}
	var bundle contentBundle
	e := json.Unmarshal(payload, &bundle)
	if e != nil {
		return nil, e
	}
	return bundle.Templates, nil
}

func (s *Service) WithSigningKey(key ed25519.PrivateKey) *Service {
	s.signingKey = append(ed25519.PrivateKey(nil), key...)
	return s
}
func signatureMessage(payload []byte, revision int64) []byte {
	sum := sha256.Sum256(payload)
	return []byte(fmt.Sprintf("azurpilot-templates-v1\n%d\n%x", revision, sum))
}
func signPayload(payload []byte, revision int64, key ed25519.PrivateKey) (string, string) {
	if len(key) != ed25519.PrivateKeySize {
		return "", ""
	}
	return base64.StdEncoding.EncodeToString(ed25519.Sign(key, signatureMessage(payload, revision))), base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey))
}

func buildBundle(ctx context.Context, tx pgx.Tx, revision int64, payload []byte) ([]byte, error) {
	templates, e := decodeTemplatePayload(payload)
	if e != nil {
		return nil, e
	}
	bundle := contentBundle{Schema: 1, Revision: revision, Templates: templates, Mappings: map[string]string{}, Aliases: map[string]string{}, HashOverrides: map[string]string{}, Thresholds: defaultMatchThresholds()}
	if len(payload) > 0 && payload[0] == '{' {
		if e = json.Unmarshal(payload, &bundle); e != nil {
			return nil, e
		}
		bundle.Revision = revision
		// Rollback restores mapping drafts as part of the same publication.
		if _, e = tx.Exec(ctx, `DELETE FROM unknown_mapping_drafts`); e != nil {
			return nil, e
		}
		for key, item := range bundle.Mappings {
			if _, e = tx.Exec(ctx, `INSERT INTO unknown_mapping_drafts(cluster_key,item) VALUES($1,$2)`, key, item); e != nil {
				return nil, e
			}
		}
		if _, e = tx.Exec(ctx, `DELETE FROM unknown_cluster_aliases`); e != nil {
			return nil, e
		}
		for from, to := range bundle.Aliases {
			if _, e = tx.Exec(ctx, `INSERT INTO unknown_cluster_aliases(from_key,to_key,revision) VALUES($1,$2,$3)`, from, to, revision); e != nil {
				return nil, e
			}
		}
		if _, e = tx.Exec(ctx, `DELETE FROM unknown_hash_overrides`); e != nil {
			return nil, e
		}
		for hash, key := range bundle.HashOverrides {
			if _, e = tx.Exec(ctx, `INSERT INTO unknown_hash_overrides(local_key,cluster_key) VALUES($1,$2)`, hash, key); e != nil {
				return nil, e
			}
		}
		if e = repairClusterRepresentatives(ctx, tx); e != nil {
			return nil, e
		}
		encoded, _ := json.Marshal(bundle.Thresholds)
		if _, e = tx.Exec(ctx, `UPDATE template_publish_settings SET thresholds=$1 WHERE id`, encoded); e != nil {
			return nil, e
		}
	} else {
		var settings []byte
		if e = tx.QueryRow(ctx, `SELECT thresholds FROM template_publish_settings WHERE id`).Scan(&settings); e != nil {
			return nil, e
		}
		if len(settings) > 0 {
			if e = json.Unmarshal(settings, &bundle.Thresholds); e != nil {
				return nil, e
			}
		}
		rows, e := tx.Query(ctx, `SELECT k.canonical_key,d.item FROM unknown_mapping_drafts d JOIN unknown_canonical_keys k ON k.key=d.cluster_key ORDER BY d.cluster_key`)
		if e != nil {
			return nil, e
		}
		for rows.Next() {
			var key, item string
			if e = rows.Scan(&key, &item); e != nil {
				rows.Close()
				return nil, e
			}
			item = stripVariant(item)
			if prior, ok := bundle.Mappings[key]; ok && prior != item {
				rows.Close()
				return nil, errors.New("conflicting mappings for a merged cluster")
			}
			bundle.Mappings[key] = item
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
		rows, e = tx.Query(ctx, `SELECT from_key,to_key FROM unknown_cluster_aliases ORDER BY from_key`)
		if e != nil {
			return nil, e
		}
		for rows.Next() {
			var from, to string
			if e = rows.Scan(&from, &to); e != nil {
				rows.Close()
				return nil, e
			}
			bundle.Aliases[from] = to
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
		// Unknown resources need observations from five devices before distribution.
		// The first acknowledged representative remains stable for a cluster.
		rows, e = tx.Query(ctx, `SELECT c.key,i.image FROM unknown_clusters c JOIN unknown_icons i ON i.icon_sha256=c.representative_sha256 WHERE c.status='pending' AND (SELECT count(DISTINCT r.payload->'source'->'metadata'->>'device_id') FROM template_candidates t JOIN candidate_sources cs ON cs.candidate_id=t.id JOIN stat_results r ON r.sha256=cs.sample_sha256 JOIN unknown_hashes h ON h.local_key=t.icon_sha256 LEFT JOIN unknown_hash_overrides o ON o.local_key=h.local_key JOIN unknown_canonical_keys k ON k.key=COALESCE(o.cluster_key,h.cluster_key) WHERE k.canonical_key=c.key)>=5 AND NOT EXISTS(SELECT 1 FROM unknown_hash_overrides o JOIN unknown_canonical_keys k ON k.key=o.cluster_key WHERE o.local_key=c.representative_sha256 AND k.canonical_key<>c.key) ORDER BY c.id LIMIT 8001`)
		if e != nil {
			return nil, e
		}
		for rows.Next() {
			var key, image string
			if e = rows.Scan(&key, &image); e != nil {
				rows.Close()
				return nil, e
			}
			raw, e := base64.StdEncoding.DecodeString(image)
			if e != nil {
				rows.Close()
				return nil, e
			}
			sum := sha256.Sum256(raw)
			bundle.Templates = append(bundle.Templates, Template{ID: hex.EncodeToString(sum[:]), ItemID: key, Name: key, Scene: "all", Enabled: true, Image: image, Kind: "temporary"})
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
	}
	if len(payload) > 0 && payload[0] == '[' {
		rows, e := tx.Query(ctx, `SELECT local_key,cluster_key FROM unknown_hash_overrides ORDER BY local_key`)
		if e != nil {
			return nil, e
		}
		for rows.Next() {
			var hash, key string
			if e = rows.Scan(&hash, &key); e != nil {
				rows.Close()
				return nil, e
			}
			bundle.HashOverrides[hash] = key
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
	}
	if len(bundle.Templates) > 10000 {
		return nil, errors.New("template bundle exceeds 10000 entries")
	}
	if _, e = tx.Exec(ctx, `UPDATE unknown_clusters c SET status=CASE
 WHEN EXISTS(SELECT 1 FROM unknown_cluster_aliases a WHERE a.from_key=c.key) THEN 'merged'
 WHEN c.representative_sha256 IS NULL THEN 'empty'
 WHEN EXISTS(SELECT 1 FROM unknown_mapping_drafts d WHERE d.cluster_key=c.key) THEN 'assigned'
 WHEN c.status IN ('assigned','merged') THEN 'pending' ELSE c.status END`); e != nil {
		return nil, e
	}
	for index := range bundle.Templates {
		t := &bundle.Templates[index]
		iconHash, normalized, err := normalizedIcon(t.Image)
		if err != nil {
			return nil, err
		}
		t.Image = normalized
		t.IconSHA256 = iconHash
		raw, e := base64.StdEncoding.DecodeString(t.Image)
		if e != nil {
			return nil, e
		}
		sum := sha256.Sum256(raw)
		t.SHA256 = hex.EncodeToString(sum[:])
		if t.Kind == "" {
			t.Kind = "formal"
		}
		if t.Kind == "formal" {
			t.ItemID = stripVariant(t.ItemID)
		}
	}
	data, e := json.Marshal(bundle)
	if e != nil {
		return nil, e
	}
	if len(data) > 32<<20 {
		return nil, errors.New("template bundle exceeds 32 MiB")
	}
	return data, nil
}

func readStoredRelease(ctx context.Context, reader unknownReader, id int64) ([]byte, string, error) {
	var payload []byte
	var digest, signature, public string
	e := reader.QueryRow(ctx, `SELECT payload,sha256,signature,public_key FROM template_releases WHERE id=$1`, id).Scan(&payload, &digest, &signature, &public)
	if e != nil {
		return nil, "", e
	}
	sum := sha256.Sum256(payload)
	if hex.EncodeToString(sum[:]) != digest {
		return nil, "", errors.New("stored template checksum verification failed")
	}
	// Rotation changes response signatures, not the historical verification key.
	if signature != "" || public != "" {
		raw, sigErr := base64.StdEncoding.DecodeString(signature)
		key, keyErr := base64.StdEncoding.DecodeString(public)
		if sigErr != nil || keyErr != nil || len(key) != ed25519.PublicKeySize || !ed25519.Verify(ed25519.PublicKey(key), signatureMessage(payload, id), raw) {
			return nil, "", errors.New("stored template signature verification failed")
		}
	}
	return payload, digest, nil
}

func (s *Service) readContentBundle(ctx context.Context, tx pgx.Tx, id int64) (contentBundle, error) {
	payload, _, e := readStoredRelease(ctx, tx, id)
	if e != nil {
		return contentBundle{}, e
	}
	var bundle contentBundle
	if len(payload) > 0 && payload[0] == '[' {
		ts, e := decodeTemplatePayload(payload)
		if e != nil {
			return bundle, e
		}
		bundle = contentBundle{Schema: 1, Revision: id, Templates: ts, Mappings: map[string]string{}, Aliases: map[string]string{}, HashOverrides: map[string]string{}, Thresholds: defaultMatchThresholds()}
	} else {
		e = json.Unmarshal(payload, &bundle)
	}
	return bundle, e
}

func (s *Service) manifest(w http.ResponseWriter, r *http.Request) {
	if len(s.signingKey) != ed25519.PrivateKeySize {
		fail(w, 503, "SIGNING_NOT_CONFIGURED", "模板签名尚未配置")
		return
	}
	var revision int64
	var digest string
	e := s.Pool.QueryRow(r.Context(), `SELECT id,sha256 FROM template_releases WHERE id=(SELECT release_id FROM template_active WHERE id)`).Scan(&revision, &digest)
	if errors.Is(e, pgx.ErrNoRows) {
		fail(w, 404, "NOT_FOUND", "尚未发布模板")
		return
	}
	if e != nil {
		internal(w, e)
		return
	}
	etag := fmt.Sprintf(`"templates-%d"`, revision)
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	body, _ := json.Marshal(map[string]any{"revision": revision, "sha256": digest, "scenes": []string{"drop", "research"}, "expires": s.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)})
	signature, public := signPayload(body, revision, s.signingKey)
	w.Header().Set("X-Content-Signature", signature)
	w.Header().Set("X-Template-Public-Key", public)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=60")
	_, _ = w.Write(body)
}

func (s *Service) delta(w http.ResponseWriter, r *http.Request) {
	if len(s.signingKey) != ed25519.PrivateKeySize {
		fail(w, 503, "SIGNING_NOT_CONFIGURED", "模板签名尚未配置")
		return
	}
	from, e := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
	if e != nil || from < 0 {
		fail(w, 400, "VALIDATION_ERROR", "无效资源版本")
		return
	}
	tx, e := s.Pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		internal(w, e)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	var active int64
	e = tx.QueryRow(r.Context(), `SELECT COALESCE(release_id,0) FROM template_active WHERE id`).Scan(&active)
	if e != nil {
		internal(w, e)
		return
	}
	if active == 0 || from > active {
		fail(w, 400, "VALIDATION_ERROR", "无效资源版本")
		return
	}
	current, e := s.readContentBundle(r.Context(), tx, active)
	if e != nil {
		internal(w, e)
		return
	}
	response := map[string]any{"schema_version": 1, "revision": active, "templates": current.Templates, "mappings": current.Mappings, "aliases": current.Aliases, "hash_overrides": current.HashOverrides, "match_thresholds": current.Thresholds}
	if from > 0 {
		prior, e := s.readContentBundle(r.Context(), tx, from)
		if errors.Is(e, pgx.ErrNoRows) {
			fail(w, 404, "NOT_FOUND", "基础版本不存在")
			return
		}
		if e != nil {
			internal(w, e)
			return
		}
		old := map[string]Template{}
		for _, entry := range prior.Templates {
			old[entry.ID] = entry
		}
		changed := []Template{}
		for _, entry := range current.Templates {
			before, exists := old[entry.ID]
			a, _ := json.Marshal(before)
			b, _ := json.Marshal(entry)
			if !exists || !bytes.Equal(a, b) {
				changed = append(changed, entry)
			}
			delete(old, entry.ID)
		}
		deleted := []string{}
		for key := range old {
			deleted = append(deleted, key)
		}
		deletedMappings, deletedAliases := []string{}, []string{}
		for key := range prior.Mappings {
			if _, ok := current.Mappings[key]; !ok {
				deletedMappings = append(deletedMappings, key)
			}
		}
		for key := range prior.Aliases {
			if _, ok := current.Aliases[key]; !ok {
				deletedAliases = append(deletedAliases, key)
			}
		}
		response["base_revision"], response["templates"], response["deleted"] = from, changed, deleted
		response["deleted_mappings"], response["deleted_aliases"] = deletedMappings, deletedAliases
		deletedOverrides := []string{}
		for hash := range prior.HashOverrides {
			if _, ok := current.HashOverrides[hash]; !ok {
				deletedOverrides = append(deletedOverrides, hash)
			}
		}
		response["deleted_hash_overrides"] = deletedOverrides
	}
	if e = tx.Commit(r.Context()); e != nil {
		internal(w, e)
		return
	}
	data, _ := json.Marshal(response)
	sum := sha256.Sum256(data)
	sig, pub := signPayload(data, active, s.signingKey)
	w.Header().Set("X-Content-SHA256", hex.EncodeToString(sum[:]))
	w.Header().Set("X-Content-Signature", sig)
	w.Header().Set("X-Template-Public-Key", pub)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func wantsBundle(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "application/vnd.azurpilot.template-bundle+json")
}
