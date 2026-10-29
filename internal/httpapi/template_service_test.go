package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ap-cloud-server/internal/config"
)

func TestTemplateAdminProxyUsesServerCredentialAndSharedAdminLogin(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/templates/status" {
			t.Errorf("path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer template-secret" {
			t.Error("browser credential leaked upstream")
		}
		_, _ = io.WriteString(w, `{"revision":42}`)
	}))
	defer upstream.Close()
	server := &Server{adminToken: "main-admin", deps: Deps{Cfg: config.Config{TemplateAdminURL: upstream.URL, TemplateAdminToken: "template-secret"}}}
	for _, token := range []string{"", "main-admin"} {
		request := httptest.NewRequest("GET", "/api/v1/admin/template-service/status", nil)
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		server.adminAuth(server.handleTemplateAdmin)(response, request)
		if token == "" && response.Code != 401 {
			t.Fatalf("no admin session: %d", response.Code)
		}
		if token != "" && (response.Code != 200 || !strings.Contains(response.Body.String(), "42")) {
			t.Fatalf("proxy failed: %d %s", response.Code, response.Body.String())
		}
	}
}

func TestTemplateAdminProxyRestrictsManagementPaths(t *testing.T) {
	server := &Server{deps: Deps{Cfg: config.Config{TemplateAdminURL: "https://templates.example", TemplateAdminToken: "service-secret"}}}
	request := httptest.NewRequest("POST", "/api/v1/admin/template-service/arbitrary", nil)
	response := httptest.NewRecorder()
	server.handleTemplateAdmin(response, request)
	if response.Code != 404 {
		t.Fatalf("unexpected proxy path accepted: %d", response.Code)
	}
}
