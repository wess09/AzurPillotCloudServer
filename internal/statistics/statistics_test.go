package statistics

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ap-cloud-server/internal/store"
)

var fixtureSequence atomic.Uint64

func fixture(t *testing.T) Result {
	t.Helper()
	// Windows clocks may return the same timestamp for consecutive fixtures.
	f := sha256.Sum256([]byte(t.Name() + time.Now().String() + jsonNumber(int64(fixtureSequence.Add(1)))))
	sum := sha256.Sum256(f[:])
	hash := hex.EncodeToString(sum[:])
	source := Source{SHA256: hash, Metadata: Metadata{DeviceID: strings.Repeat("a", 32), InstanceID: strings.Repeat("b", 16), Project: "drop", Scene: "test_" + hash[:12], Server: "cn", CollectedAt: time.Now().UTC().Add(-time.Minute).Format(time.RFC3339), Sizes: []int64{8}}, Files: []SourceFile{{Index: 0, Size: 8, SHA256: hex.EncodeToString(f[:]), FileID: 1}}}
	return Result{SHA256: hash, Revision: 1, Status: "complete", Parser: "test-v1", Source: source, Events: []Event{{Type: "drop", Duration: 3600, Items: []Item{{Item: "Coins", Amount: 10}}}}}
}
func service(t *testing.T) *Service {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	st, e := store.Open(context.Background(), url, 24*time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(st.Close)
	return New(st.Pool, "worker-token", "admin-token")
}
func call(t *testing.T, s *Service, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var data []byte
	if body != nil {
		var e error
		data, e = json.Marshal(body)
		if e != nil {
			t.Fatal(e)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	m := http.NewServeMux()
	s.Register(m)
	response := httptest.NewRecorder()
	m.ServeHTTP(response, req)
	return response
}

func TestResultValidation(t *testing.T) {
	valid := fixture(t)
	if e := valid.validate(); e != nil {
		t.Fatal(e)
	}
	bad := valid
	bad.Source = valid.Source
	bad.Source.Files = append([]SourceFile{}, valid.Source.Files...)
	bad.Source.Files[0].SHA256 = strings.Repeat("0", 64)
	if bad.validate() == nil {
		t.Fatal("accepted a forged source fingerprint")
	}
	bad = valid
	bad.Events = []Event{{Type: "drop", Items: []Item{{Item: "Coins", Amount: 0}}}}
	if bad.validate() == nil {
		t.Fatal("accepted zero item count")
	}
	bad = valid
	bad.Diagnostics = []string{"missing_template"}
	if bad.validate() == nil {
		t.Fatal("accepted incomplete formal statistics")
	}
}

func TestConfiguredSourceLimits(t *testing.T) {
	r := fixture(t)
	r.Source.Files[0].Size = 20 << 20
	r.Source.Metadata.Sizes[0] = 20 << 20
	if r.validate() == nil {
		t.Fatal("default file limit ignored")
	}
	if e := r.validateLimits(20<<20, 40<<20); e != nil {
		t.Fatal(e)
	}
	if r.validateLimits(20<<20, 19<<20) == nil {
		t.Fatal("group limit ignored")
	}
}

func TestNumericEquipmentIdentifiers(t *testing.T) {
	r := fixture(t)
	for _, name := range []string{"Complete RepairPack", "TripleComplete RepairPack", "533mm_Quintuple_Torpedo_Mount_T2"} {
		r.Events[0].Items[0].Item = name
		if err := r.validate(); err != nil {
			t.Fatalf("rejected an existing Alas resource identifier %q: %v", name, err)
		}
	}
	r.Events[0].Items[0].Item = "533mm_Quintuple_Torpedo_Mount_T2"
	if err := r.validate(); err != nil {
		t.Fatal("rejected a numeric equipment identifier", err)
	}
	template := Template{ItemID: r.Events[0].Items[0].Item, Name: "鱼雷设计图", Category: "equipment_blueprint", Scene: "research"}
	if err := validateTemplate(template); err != nil {
		t.Fatal("rejected the corresponding template", err)
	}
	r.Events[0].Items = append(r.Events[0].Items, r.Events[0].Items[0])
	if r.validate() == nil {
		t.Fatal("accepted duplicate equipment rewards")
	}
	for _, name := range []string{"../item", "item/name", "", "_item"} {
		r.Events[0].Items = []Item{{Item: name, Amount: 1}}
		if r.validate() == nil {
			t.Fatalf("accepted invalid item %q", name)
		}
	}
}

func TestAuthenticatedPreviewAndInvalidIndex(t *testing.T) {
	s := service(t)
	result := fixture(t)
	if _, e := s.Apply(context.Background(), result); e != nil {
		t.Fatal(e)
	}
	var hits atomic.Int32
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("Authorization") != "Bearer preview-token" {
			t.Error("preview credential missing")
		}
		var body struct {
			Source Source `json:"source"`
			Index  int    `json:"file_index"`
			Frame  int    `json:"frame_index"`
		}
		if e := json.NewDecoder(r.Body).Decode(&body); e != nil || body.Source.SHA256 != result.SHA256 || body.Index != 0 || body.Frame != 0 {
			t.Error("invalid preview source", e)
		}
		w.Header().Set("Content-Type", "image/png")
		_ = png.Encode(w, image.NewRGBA(image.Rect(0, 0, 16, 16)))
	}))
	defer worker.Close()
	s.WithWorker(worker.URL, "preview-token")
	path := "/api/v1/admin/statistics/preview/" + result.SHA256 + "/0?frame=0"
	if r := call(t, s, "GET", path, "", nil); r.Code != 401 {
		t.Fatal(r.Code)
	}
	if r := call(t, s, "GET", strings.Replace(path, "/0?", "/invalid?", 1), "admin-token", nil); r.Code != 400 {
		t.Fatal(r.Code)
	}
	r := call(t, s, "GET", path, "admin-token", nil)
	if r.Code != 200 || r.Header().Get("Cache-Control") != "no-store" || hits.Load() != 1 {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	if _, e := png.Decode(bytes.NewReader(r.Body.Bytes())); e != nil {
		t.Fatal(e)
	}
}
func TestResultReplacementAndConcurrentReplay(t *testing.T) {
	s := service(t)
	r := fixture(t)
	var accepted, duplicates atomic.Int32
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			ack, e := s.Apply(context.Background(), r)
			if e != nil {
				t.Error(e)
				return
			}
			switch ack.Status {
			case "accepted":
				accepted.Add(1)
			case "duplicate":
				duplicates.Add(1)
			default:
				t.Errorf("unexpected %+v", ack)
			}
		})
	}
	wg.Wait()
	if accepted.Load() != 1 || duplicates.Load() != 11 {
		t.Fatalf("not idempotent: %d %d", accepted.Load(), duplicates.Load())
	}
	changed := r
	changed.Events = []Event{{Type: "drop", Duration: 3600, Items: []Item{{Item: "Coins", Amount: 4}}}}
	ack, e := s.Apply(context.Background(), changed)
	if e != nil || ack.Status != "conflict" {
		t.Fatalf("same revision overwrote: %+v %v", ack, e)
	}
	changed.Revision = 2
	ack, e = s.Apply(context.Background(), changed)
	if e != nil || ack.Status != "accepted" {
		t.Fatalf("replacement failed %+v %v", ack, e)
	}
	rec := call(t, s, "GET", "/api/v2/get/drop?scene="+r.Source.Metadata.Scene, "", nil)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var summary struct {
		Items []struct {
			Amount  int64   `json:"amount"`
			Rate    float64 `json:"drop_rate"`
			Average float64 `json:"average_per_sample"`
		} `json:"items"`
	}
	if e = json.Unmarshal(rec.Body.Bytes(), &summary); e != nil {
		t.Fatal(e)
	}
	if len(summary.Items) != 1 || summary.Items[0].Amount != 4 || summary.Items[0].Rate != 1 || summary.Items[0].Average != 4 {
		t.Fatalf("wrong contribution: %s", rec.Body.String())
	}
	changed.Revision = 3
	changed.Status = "needs_review"
	changed.Diagnostics = []string{"uncertain_amount"}
	ack, e = s.Apply(context.Background(), changed)
	if e != nil || ack.Status != "accepted" {
		t.Fatal(ack, e)
	}
	rec = call(t, s, "GET", "/api/v2/get/drop?scene="+r.Source.Metadata.Scene, "", nil)
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("incomplete sample stayed in stats: %s", rec.Body.String())
	}
}
func TestStatisticsAuthenticationAndBounds(t *testing.T) {
	s := service(t)
	for _, path := range []string{"/api/v1/admin/statistics/overview", "/internal/v1/statistics/results/" + strings.Repeat("a", 64)} {
		rec := call(t, s, "GET", path, "", nil)
		if rec.Code != 401 {
			t.Fatalf("unauthenticated %s: %d", path, rec.Code)
		}
	}
	for _, query := range []string{"series=-1", "series=1&series=2", "from=bad", "unknown=1", "from=2020-01-01T00:00:00Z"} {
		rec := call(t, s, "GET", "/api/v2/get/research?"+query, "", nil)
		if rec.Code != 400 {
			t.Fatalf("accepted query %s: %s", query, rec.Body.String())
		}
	}
	rec := call(t, s, "POST", "/internal/v1/statistics/results", "worker-token", map[string]any{"results": []Result{}})
	if rec.Code != 400 {
		t.Fatal(rec.Body.String())
	}
}
func TestCandidateClassificationPublicationAndRollback(t *testing.T) {
	s := service(t)
	r := fixture(t)
	r.Status = "needs_review"
	r.Diagnostics = []string{"missing_template"}
	if ack, e := s.Apply(context.Background(), r); e != nil || ack.Status != "accepted" {
		t.Fatal(ack, e)
	}
	im := image.NewRGBA(image.Rect(0, 0, 49, 49))
	for x := range 49 {
		for y := range 49 {
			im.Set(x, y, color.RGBA{uint8(x * 5), uint8(y * 5), uint8(len(t.Name())), 255})
		}
	}
	var pngBytes bytes.Buffer
	if e := png.Encode(&pngBytes, im); e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(pngBytes.Bytes())
	id := hex.EncodeToString(sum[:])
	candidate := Candidate{ID: id, Scene: "drop", Image: base64.StdEncoding.EncodeToString(pngBytes.Bytes()), Sample: r.SHA256, Occurrence: "0:0:0", Metadata: map[string]any{"file_index": 0, "frame_index": 0}}
	for range 2 {
		rec := call(t, s, "POST", "/internal/v1/template-candidates", "worker-token", candidate)
		if rec.Code != 200 {
			t.Fatal(rec.Body.String())
		}
	}
	var count int64
	if e := s.Pool.QueryRow(context.Background(), `SELECT count(*) FROM candidate_sources WHERE candidate_id=$1 AND sample_sha256=$2`, id, r.SHA256).Scan(&count); e != nil || count != 1 {
		t.Fatal("candidate replay increased occurrence", count, e)
	}
	template := Template{ItemID: "TestCoins", Name: "物资", Category: "consumable", Scene: "drop", Enabled: true}
	rec := call(t, s, "PATCH", "/api/v1/admin/template-candidates/"+id, "admin-token", map[string]any{"status": "assigned", "template": template})
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var activation struct {
		Release int64 `json:"release_id"`
		Queued  int64 `json:"queued_samples"`
	}
	if e := json.Unmarshal(rec.Body.Bytes(), &activation); e != nil || activation.Release < 1 || activation.Queued < 1 {
		t.Fatal("classification did not automatically publish and queue unresolved samples", rec.Body.String(), e)
	}
	var active int64
	if e := s.Pool.QueryRow(context.Background(), `SELECT release_id FROM template_active WHERE id`).Scan(&active); e != nil || active != activation.Release {
		t.Fatal("classification and active release were not committed together", active, e)
	}
	rec = call(t, s, "POST", "/api/v1/admin/templates/test", "admin-token", map[string]any{"ids": []string{id}})
	if rec.Code != 202 {
		t.Fatal(rec.Body.String())
	}
	rec = call(t, s, "POST", "/api/v1/admin/templates/publish", "admin-token", nil)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var published struct {
		ID int64 `json:"release_id"`
	}
	if e := json.Unmarshal(rec.Body.Bytes(), &published); e != nil {
		t.Fatal(e)
	}
	rec = call(t, s, "GET", "/internal/v1/template-releases/"+jsonNumber(published.ID), "worker-token", nil)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	digest := sha256.Sum256(rec.Body.Bytes())
	if rec.Header().Get("X-Content-SHA256") != hex.EncodeToString(digest[:]) {
		t.Fatal("release checksum mismatch")
	}
	rec = call(t, s, "POST", "/api/v1/admin/templates/rollback", "admin-token", map[string]any{"release_id": published.ID})
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var rollback struct {
		ID int64 `json:"release_id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &rollback)
	if rollback.ID <= published.ID {
		t.Fatal("rollback must be a new activation version")
	}
	rec = call(t, s, "POST", "/internal/v1/worker/sync", "worker-token", map[string]any{"worker_id": "integration-test", "release_id": 0, "pending": 1, "updates": []any{}})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"commands"`) {
		t.Fatal(rec.Body.String())
	}
}
func jsonNumber(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestCandidateAutomaticallyResolvesAndReopens(t *testing.T) {
	s := service(t)
	ctx := context.Background()
	var data bytes.Buffer
	icon := image.NewNRGBA(image.Rect(0, 0, 49, 49))
	for offset := 3; offset < len(icon.Pix); offset += 4 {
		icon.Pix[offset] = 255
	}
	if e := png.Encode(&data, icon); e != nil {
		t.Fatal(e)
	}
	digest := sha256.Sum256(data.Bytes())
	id := hex.EncodeToString(digest[:])
	r1, r2 := fixture(t), fixture(t)
	for _, result := range []*Result{&r1, &r2} {
		result.Status = "needs_review"
		result.Candidates = []string{id}
		result.Diagnostics = []string{"missing_template:" + id}
		if _, e := s.Apply(ctx, *result); e != nil {
			t.Fatal(e)
		}
		candidate := Candidate{ID: id, Scene: "drop", Image: base64.StdEncoding.EncodeToString(data.Bytes()), Sample: result.SHA256, Occurrence: "0:0:0", Metadata: map[string]any{}}
		if rec := call(t, s, "POST", "/internal/v1/template-candidates", "worker-token", candidate); rec.Code != 200 {
			t.Fatal(rec.Body.String())
		}
	}
	expect := func(status string, blocked int64) {
		t.Helper()
		rec := call(t, s, "GET", "/api/v1/admin/template-candidates?status="+status, "admin-token", nil)
		var reply struct {
			Items []struct {
				ID      string `json:"id"`
				Status  string `json:"status"`
				Blocked int64  `json:"blocked_samples"`
			} `json:"items"`
		}
		if e := json.Unmarshal(rec.Body.Bytes(), &reply); e != nil || rec.Code != 200 {
			t.Fatal(rec.Body.String(), e)
		}
		for _, candidate := range reply.Items {
			if candidate.ID == id {
				if candidate.Status != status || candidate.Blocked != blocked {
					t.Fatal(candidate)
				}
				return
			}
		}
		t.Fatalf("candidate missing from %s", status)
	}
	expect("pending", 2)
	r1.Revision++
	r1.Status = "complete"
	r1.Candidates = nil
	r1.Diagnostics = nil
	if _, e := s.Apply(ctx, r1); e != nil {
		t.Fatal(e)
	}
	expect("pending", 1)
	// The icon is fixed even though a different project error still needs review.
	r2.Revision++
	r2.Candidates = nil
	r2.Diagnostics = []string{"unknown_project"}
	if _, e := s.Apply(ctx, r2); e != nil {
		t.Fatal(e)
	}
	expect("resolved", 0)
	rec := call(t, s, "GET", "/api/v1/admin/template-candidates?status=pending", "admin-token", nil)
	if strings.Contains(rec.Body.String(), id) {
		t.Fatal("resolved candidate still occupies pending list")
	}
	// A later rollback can make the exact icon unknown again.
	r1.Revision++
	r1.Status = "needs_review"
	r1.Candidates = []string{id}
	r1.Diagnostics = []string{"missing_template:" + id}
	if _, e := s.Apply(ctx, r1); e != nil {
		t.Fatal(e)
	}
	expect("pending", 1)
}

func TestReprocessUnresolvedMergesConcurrentClicks(t *testing.T) {
	s := service(t)
	if rec := call(t, s, "POST", "/api/v1/admin/templates/publish", "admin-token", nil); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	unresolved := fixture(t)
	unresolved.Status = "needs_review"
	unresolved.Diagnostics = []string{"missing_template:" + strings.Repeat("d", 64)}
	unresolved.Candidates = []string{strings.Repeat("d", 64)}
	parserFailure := fixture(t)
	parserFailure.Status = "needs_review"
	parserFailure.Diagnostics = []string{"unknown_project_without_candidate"}
	invalid := fixture(t)
	invalid.Status, invalid.Events = "invalid", nil
	invalid.Diagnostics = []string{"duplicate_research_list_capture"}
	complete := fixture(t)
	for _, result := range []Result{unresolved, invalid, complete, parserFailure} {
		if _, e := s.Apply(context.Background(), result); e != nil {
			t.Fatal(e)
		}
	}
	if rec := call(t, s, "POST", "/api/v1/admin/statistics/reprocess-unresolved", "", nil); rec.Code != 401 {
		t.Fatalf("unprotected bulk reprocessing: %d", rec.Code)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if rec := call(t, s, "POST", "/api/v1/admin/statistics/reprocess-unresolved", "admin-token", nil); rec.Code != 202 {
				t.Error(rec.Body.String())
			}
		})
	}
	wg.Wait()
	for _, result := range []Result{unresolved, invalid, complete, parserFailure} {
		var count int
		e := s.Pool.QueryRow(context.Background(), `SELECT count(*) FROM worker_commands WHERE action='reprocess' AND payload->'sources' @> $1::jsonb`, "[{\"sha256\":\""+result.SHA256+"\"}]").Scan(&count)
		want := 0
		if len(result.Candidates) > 0 {
			want = 1
		}
		if e != nil || count != want {
			t.Fatalf("%s: got %d commands, want %d: %v", result.Status, count, want, e)
		}
	}
	// Republishing an unchanged package must not scan failures again.
	if rec := call(t, s, "POST", "/api/v1/admin/templates/publish", "admin-token", nil); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var versions int
	if e := s.Pool.QueryRow(context.Background(), `SELECT count(*) FROM worker_commands WHERE action='reprocess' AND payload->'sources' @> $1::jsonb`, "[{\"sha256\":\""+unresolved.SHA256+"\"}]").Scan(&versions); e != nil || versions != 1 {
		t.Fatal("unchanged publication unnecessarily repeated a failure", versions, e)
	}
	// Count original files and submissions separately without breaking old clients.
	rec := call(t, s, "GET", "/api/v1/admin/statistics/overview", "admin-token", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"image_counts"`) || !strings.Contains(rec.Body.String(), `"counts"`) {
		t.Fatal(rec.Body.String())
	}
}

func TestTemplatePublicationOnlyRetriesRelevantFailures(t *testing.T) {
	s := service(t)
	ctx := context.Background()
	icon := image.NewNRGBA(image.Rect(0, 0, 49, 49))
	for offset := 3; offset < len(icon.Pix); offset += 4 {
		icon.Pix[offset] = 255
	}
	var encoded bytes.Buffer
	if e := png.Encode(&encoded, icon); e != nil {
		t.Fatal(e)
	}
	validImage := base64.StdEncoding.EncodeToString(encoded.Bytes())
	a := Template{ID: strings.Repeat("a", 64), ItemID: "Coins", Enabled: true, Scene: "all", Image: validImage}
	b := Template{ID: strings.Repeat("b", 64), ItemID: "Chips", Enabled: true, Scene: "all", Image: validImage}
	c := Template{ID: strings.Repeat("c", 64), ItemID: "NewItem", Enabled: true, Scene: "all", Image: validImage}
	activate := func(templates []Template) {
		t.Helper()
		payload, e := json.Marshal(templates)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.createRelease(ctx, payload); e != nil {
			t.Fatal(e)
		}
	}
	activate([]Template{a, b})
	missing, parser, usedA, usedB := fixture(t), fixture(t), fixture(t), fixture(t)
	missing.Status = "needs_review"
	missing.Candidates = []string{c.ID}
	missing.Diagnostics = []string{"missing_template:" + c.ID}
	parser.Status = "needs_review"
	parser.Diagnostics = []string{"no_complete_reward_frame"}
	usedA.UsedTemplates = []string{a.ID}
	usedB.UsedTemplates = []string{b.ID}
	for _, r := range []Result{missing, parser, usedA, usedB} {
		if _, e := s.Apply(ctx, r); e != nil {
			t.Fatal(e)
		}
	}
	expect := func(wantMissing, wantA int) {
		t.Helper()
		for i, r := range []Result{missing, parser, usedA, usedB} {
			var count int
			if e := s.Pool.QueryRow(ctx, `SELECT count(*) FROM worker_commands WHERE action='reprocess' AND payload->'sources' @> $1::jsonb`, "[{\"sha256\":\""+r.SHA256+"\"}]").Scan(&count); e != nil {
				t.Fatal(e)
			}
			want := 0
			if i == 0 {
				want = wantMissing
			}
			if i == 2 {
				want = wantA
			}
			if count != want {
				t.Fatalf("source %d: got %d, want %d", i, count, want)
			}
		}
	}
	activate([]Template{a, b, c})
	expect(1, 0) // New template: only missing icons.
	a.ItemID = "CorrectedCoins"
	activate([]Template{a, b, c})
	expect(2, 1) // Changed matcher: users of A, not B.
	a.Enabled = false
	activate([]Template{a, b, c})
	expect(2, 2) // Removal cannot repair missing icons.
	a.Enabled = true
	activate([]Template{a, b, c})
	expect(3, 3) // Rollback/re-enable restores matcher.
	b.Name = "display-only change"
	activate([]Template{a, b, c})
	expect(3, 3)
	activate([]Template{a, b, c})
	expect(3, 3)
	for _, reason := range []string{"missing_template", "parser_failure"} {
		rec := call(t, s, "GET", "/api/v1/admin/statistics/results?status=needs_review&reason="+reason, "admin-token", nil)
		if rec.Code != 200 {
			t.Fatal(rec.Body.String())
		}
		wanted, excluded := missing.SHA256, parser.SHA256
		if reason == "parser_failure" {
			wanted, excluded = excluded, wanted
		}
		if !strings.Contains(rec.Body.String(), wanted) || strings.Contains(rec.Body.String(), excluded) {
			t.Fatal("failure categories mixed", rec.Body.String())
		}
	}
}
