package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ap-cloud-server/internal/templateproxy"
)

func TestV3UnknownTemplateForwarding(t *testing.T) {
	var gotAuth, gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"schema_version":3,"status":"registered","cluster_key":"U-abc"}`)
	}))
	defer upstream.Close()

	ingest, err := templateproxy.NewIngest(upstream.URL+"/api/v3/post/templates/unknown", "ingest-secret")
	if err != nil {
		t.Fatal(err)
	}
	srv, _ := newTestServer(t, &fakeBackend{}, nil)
	srv.deps.TemplateIngest = ingest

	payload := `{"scene":"commission","image_base64":"aGk="}`
	req := httptest.NewRequest(http.MethodPost, "/api/v3/post/templates/unknown", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码异常: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "U-abc") {
		t.Fatalf("响应未透传: %s", rec.Body.String())
	}
	if gotAuth != "Bearer ingest-secret" {
		t.Fatalf("上传凭据未转发: %q", gotAuth)
	}
	if gotBody != payload {
		t.Fatalf("请求体未透传: %s", gotBody)
	}
}

func TestV3UnknownTemplateDisabled(t *testing.T) {
	srv, _ := newTestServer(t, &fakeBackend{}, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/v3/post/templates/unknown", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("未配置上传时应为 503，实际 %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "TEMPLATE_INGEST_DISABLED") {
		t.Fatalf("错误码异常: %s", rec.Body.String())
	}
}

func TestV3UnknownTemplateRejectsOversize(t *testing.T) {
	ingest, err := templateproxy.NewIngest("http://127.0.0.1:1/api/v3/post/templates/unknown", "x")
	if err != nil {
		t.Fatal(err)
	}
	srv, _ := newTestServer(t, &fakeBackend{}, nil)
	srv.deps.TemplateIngest = ingest

	big := strings.Repeat("a", v3UnknownMaxBytes+1)
	req := httptest.NewRequest(http.MethodPost, "/api/v3/post/templates/unknown", strings.NewReader(big))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超限请求应为 413，实际 %d", rec.Code)
	}
}

func TestV3TemplateUpstreamUnavailable(t *testing.T) {
	srv, _ := newTestServer(t, &fakeBackend{}, nil)
	p, err := templateproxy.New("", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	srv.deps.Templates = p
	req := httptest.NewRequest(http.MethodGet, "/api/v3/get/templates/manifest", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("上游未配置时应为 503，实际 %d", rec.Code)
	}
}
