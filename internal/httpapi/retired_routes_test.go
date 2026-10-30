package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestRetiredABFrontendAndRESTManagementRoutesAreGone(t *testing.T) {
	server, _ := newTestServer(t, &fakeBackend{}, nil)
	paths := []string{"/", "/admin", "/admin/", "/admin/assets/old.js", "/api/v2/get/research", "/api/v2/get/research/options", "/api/v2/get/research/time-slots", "/api/v2/get/drop", "/api/v2/get/drop/options", "/api/v2/get/drop/time-slots", "/api/upload", "/api/upload/", "/api/v1/uploads/init", "/api/v1/uploads/00000000-0000-4000-8000-000000000001/files/0", "/api/v1/uploads/00000000-0000-4000-8000-000000000001/complete", "/api/v2/uploads/init", "/api/v2/uploads/00000000-0000-4000-8000-000000000001/files/0/prepare", "/api/v2/uploads/00000000-0000-4000-8000-000000000001/files/0/complete", "/api/v2/uploads/00000000-0000-4000-8000-000000000001/complete", "/api/v1/admin/statistics/overview", "/api/v1/admin/statistics/results", "/api/v1/admin/statistics/preview/abc/0", "/api/v1/admin/worker-commands", "/api/v1/admin/statistics/reprocess", "/api/v1/admin/statistics/reprocess-unresolved", "/internal/v1/statistics/results", "/internal/v1/statistics/results/abc", "/api/v1/admin/announcement", "/api/v1/admin/update-policy", "/api/v1/admin/maintenance", "/api/v1/admin/maintenance/source", "/api/v1/admin/maintenance/cn", "/api/v1/admin/publish/status"}
	for _, path := range paths {
		for _, method := range []string{"GET", "POST", "PUT", "DELETE"} {
			r := httptest.NewRequest(method, path, unreadTemplateBody{})
			r.Header.Set("Authorization", "Bearer test-admin-token")
			w := httptest.NewRecorder()
			server.Handler().ServeHTTP(w, r)
			if w.Code != 404 {
				t.Errorf("retired route remains: %s %s: %d", method, path, w.Code)
			}
		}
	}
}

func TestManagementCommandsPreserveValidationAndMutation(t *testing.T) {
	backend := &fakeBackend{}
	server, _ := newTestServer(t, backend, nil)
	ctx := context.Background()
	if _, err := server.executeAdmin(ctx, "update_policy.set", json.RawMessage(`{"force_update":false}`)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{}`, `{"force_update":"yes"}`, `{"force_update":true,"extra":1}`} {
		if _, err := server.executeAdmin(ctx, "update_policy.set", json.RawMessage(raw)); err == nil {
			t.Fatal("invalid parameters accepted", raw)
		}
	}
	if _, err := server.executeAdmin(ctx, "announcement.publish", json.RawMessage(`{"title":"Test","content":"Message"}`)); err != nil {
		t.Fatal(err)
	}
	if backend.announcement == nil || backend.announcement.Title != "Test" {
		t.Fatal("announcement was not persisted")
	}
	if _, err := server.executeAdmin(ctx, "maintenance.save", json.RawMessage(`{"server":"cn","name":"test","starts_at":"2026-10-30T10:00:00Z","ends_at":"2026-10-29T10:00:00Z"}`)); err == nil {
		t.Fatal("invalid time range accepted")
	}
	if _, err := server.executeAdmin(ctx, "arbitrary", nil); err == nil {
		t.Fatal("unknown management method accepted")
	}
}
