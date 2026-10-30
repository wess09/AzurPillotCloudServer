package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ap-cloud-server/internal/store"
)

func TestLegacyAPTelemetryContract(t *testing.T) {
	b := &fakeBackend{outcome: store.Cl1Accepted}
	s, _ := newTestServer(t, b, nil)
	body := `{"device_id":"0123456789abcdef0123456789abcdef","instance_id":"0123456789abcdef","month":"2026-10","battle_count":1200,"battle_rounds":1,"sortie_cost":1,"akashi_encounters":60,"akashi_probability":999,"average_stamina":999,"net_stamina_gain":4200}`
	post := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/telemetry", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	w := post(body)
	if w.Code != 200 || b.submitted == nil || b.submitted.AkashiAP != 4200 || b.submitted.BattleRounds != 600 || !b.submitted.Legacy || !strings.Contains(w.Body.String(), `"status":"success"`) {
		t.Fatal(w.Code, w.Body, b.submitted)
	}
	id, hash := b.submitted.RequestID, b.submittedHash
	w = post(strings.Replace(body, `"average_stamina":999`, `"average_stamina":0`, 1))
	if w.Code != 200 || id != b.submitted.RequestID || hash != b.submittedHash {
		t.Fatal("derived fields changed idempotency", w.Body)
	}
	if w = post(strings.Replace(body, `"net_stamina_gain":4200`, `"net_stamina_gain":-1`, 1)); w.Code != 400 {
		t.Fatal(w.Code, w.Body)
	}
}

func TestLegacyAPReadsAndUploadRedirect(t *testing.T) {
	content := "hello"
	b := &fakeBackend{announcement: &store.Announcement{ID: "notice-1", Title: "test", Content: &content}, force: true, maintenance: &store.Maintenance{Name: "maintenance", StartsAt: time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)}}
	s, _ := newTestServer(t, b, nil)
	for _, c := range []struct{ path, want string }{{"/api/get/announcement", `"announcementId":"notice-1"`}, {"/api/get/announcement?id=notice-1", "{}"}, {"/api/force_update", "true"}, {"/api/maintenance", `"timezone":"UTC+9"`}} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest("GET", c.path, nil))
		if w.Code != 200 || !strings.Contains(w.Body.String(), c.want) {
			t.Fatal(c.path, w.Code, w.Body)
		}
	}
}
