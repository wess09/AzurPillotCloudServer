package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ap-cloud-server/internal/statistics"
)

type unreadTemplateBody struct{}

func (unreadTemplateBody) Read([]byte) (int, error) { panic("main server read template upload") }

func TestMainServerDoesNotExposeTemplateTransport(t *testing.T) {
	server, _ := newTestServer(t, &fakeBackend{}, nil)
	legacy := http.NewServeMux()
	statistics.New(nil, "worker-token", "main-admin").Register(legacy)
	routes := []struct{ method, path string }{
		{"POST", "/api/v3/post/templates/unknown"},
		{"POST", "/api/v3/post/unknown-candidates"},
		{"GET", "/api/v3/get/templates/manifest"},
		{"GET", "/api/v3/get/templates/delta?from=0"},
		{"GET", "/api/v3/get/mappings"},
		{"GET", "/api/v1/admin/template-service/status"},
		{"POST", "/api/v1/admin/template-service/templates"},
		{"PUT", "/api/v1/admin/template-service/mappings/U-123"},
		{"POST", "/api/v1/admin/template-service/publish"},
		{"POST", "/internal/v1/template-candidates"},
		{"POST", "/internal/v1/worker/sync"},
		{"GET", "/internal/v1/template-releases/1"},
		{"GET", "/api/v1/admin/template-candidates"},
		{"PATCH", "/api/v1/admin/template-candidates/1"},
		{"GET", "/api/v1/admin/templates"},
		{"GET", "/api/v1/admin/template-releases"},
		{"POST", "/api/v1/admin/templates/publish"},
		{"POST", "/api/v1/admin/templates/rollback"},
		{"POST", "/api/v1/admin/templates/test"},
	}
	for _, handler := range []http.Handler{server.Handler(), legacy} {
		for _, route := range routes {
			request := httptest.NewRequest(route.method, route.path, unreadTemplateBody{})
			request.Header.Set("Authorization", "Bearer main-admin")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusNotFound {
				t.Errorf("template route still exposed: %s %s: %d", route.method, route.path, response.Code)
			}
		}
	}
}

func TestTemplateEventReceiverRemainsAuthenticated(t *testing.T) {
	server, _ := newTestServer(t, &fakeBackend{}, nil)
	request := httptest.NewRequest("POST", "/internal/v1/templates/events", strings.NewReader(`{}`))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured event receiver should fail closed: %d", response.Code)
	}
}
