package statistics

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
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
	f := sha256.Sum256([]byte(t.Name() + time.Now().String() + strconv.FormatUint(fixtureSequence.Add(1), 10)))
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
func TestReprocessUnresolvedMergesConcurrentClicks(t *testing.T) {
	s := service(t)
	if _, err := s.createRelease(context.Background(), []byte("[]")); err != nil {
		t.Fatal(err)
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
	if _, err := s.createRelease(context.Background(), []byte("[]")); err != nil {
		t.Fatal(err)
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
	a := Template{ID: strings.Repeat("a", 64), ItemID: "Coins", Enabled: true, Scene: "all", Image: "a"}
	b := Template{ID: strings.Repeat("b", 64), ItemID: "Chips", Enabled: true, Scene: "all", Image: "b"}
	c := Template{ID: strings.Repeat("c", 64), ItemID: "NewItem", Enabled: true, Scene: "all", Image: "c"}
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
