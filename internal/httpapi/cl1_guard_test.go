package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"

	"ap-cloud-server/internal/store"
)

func TestCl1SilentIntakeOutcomes(t *testing.T) {
	var ordinary string
	for _, outcome := range []store.Cl1Outcome{store.Cl1Accepted, store.Cl1Observed, store.Cl1Discarded} {
		s, _ := newTestServer(t, &fakeBackend{outcome: outcome}, nil)
		w := postCl1(s, validCl1Body, nil)
		if w.Code != 200 {
			t.Fatal("intake state disclosed by HTTP status", outcome, w.Code)
		}
		if ordinary == "" {
			ordinary = w.Body.String()
		} else if ordinary != w.Body.String() {
			t.Fatal("intake state disclosed by acknowledgement", outcome, w.Body)
		}
		r := httptest.NewRequest("POST", "/api/telemetry", strings.NewReader(`{"device_id":"0123456789abcdef0123456789abcdef","instance_id":"0123456789abcdef","month":"2026-10","battle_count":1200,"akashi_encounters":60,"net_stamina_gain":4200}`))
		r.Header.Set("Content-Type", "application/json")
		legacy := httptest.NewRecorder()
		s.Handler().ServeHTTP(legacy, r)
		if legacy.Code != 200 || !strings.Contains(legacy.Body.String(), `"status":"success"`) {
			t.Fatal("legacy intake leaked state", legacy.Code, legacy.Body)
		}
	}
}
