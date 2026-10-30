package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ap-cloud-server/internal/gateway"
	"ap-cloud-server/internal/store"
)

func get(s *Server, path string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestGetServer(t *testing.T) {
	query := func(ctx context.Context, region string) ([]gateway.RawServer, error) {
		if region == "jp" {
			return nil, &gateway.RegionError{Code: "timeout"}
		}
		return []gateway.RawServer{{ID: 28, Name: "长弓计划", State: 0}}, nil
	}
	backend := &fakeBackend{}
	srv, _ := newTestServer(t, backend, query)
	headers := map[string]string{"X-AzurPilot-ID": testDeviceID}

	t.Run("正常返回", func(t *testing.T) {
		rec := get(srv, "/api/v2/get/server?region=cn&server_id=28", headers)
		if rec.Code != http.StatusOK {
			t.Fatalf("期望 200，得到 %d: %s", rec.Code, rec.Body.String())
		}
		var resp struct {
			SchemaVersion int    `json:"schema_version"`
			Region        string `json:"region"`
			Server        struct {
				ID     int    `json:"id"`
				Name   string `json:"name"`
				Status string `json:"status"`
			} `json:"server"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.SchemaVersion != 2 || resp.Region != "cn" || resp.Server.ID != 28 ||
			resp.Server.Name != "长弓计划" || resp.Server.Status != "normal" {
			t.Fatalf("响应错误: %+v", resp)
		}
	})
	t.Run("region 非法返回 400", func(t *testing.T) {
		rec := get(srv, "/api/v2/get/server?region=kr&server_id=28", headers)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "VALIDATION_ERROR") {
			t.Fatalf("期望 400 VALIDATION_ERROR: %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("server_id 非法返回 400", func(t *testing.T) {
		rec := get(srv, "/api/v2/get/server?region=cn&server_id=abc", headers)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("期望 400: %d", rec.Code)
		}
	})
	t.Run("服务器不存在返回 404", func(t *testing.T) {
		rec := get(srv, "/api/v2/get/server?region=cn&server_id=999", headers)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "SERVER_NOT_FOUND") {
			t.Fatalf("期望 404 SERVER_NOT_FOUND: %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("网关不可用返回 503", func(t *testing.T) {
		rec := get(srv, "/api/v2/get/server?region=jp&server_id=28", headers)
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "SOURCE_UNAVAILABLE") {
			t.Fatalf("期望 503 SOURCE_UNAVAILABLE: %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("缺少设备头返回 400", func(t *testing.T) {
		rec := get(srv, "/api/v2/get/server?region=cn&server_id=28", nil)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "INVALID_DEVICE_ID") {
			t.Fatalf("期望 400 INVALID_DEVICE_ID: %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("错误方法返回 405", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v2/get/server?region=cn&server_id=28", nil)
		req.Header.Set("X-AzurPilot-ID", testDeviceID)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("期望 405: %d", rec.Code)
		}
		if allow := rec.Header().Get("Allow"); allow != http.MethodGet {
			t.Fatalf("期望 Allow: GET，得到 %q", allow)
		}
	})
}

func TestGetMaintenance(t *testing.T) {
	t.Run("无维护安排", func(t *testing.T) {
		backend := &fakeBackend{}
		srv, _ := newTestServer(t, backend, nil)
		rec := get(srv, "/api/v2/get/maintenance?server=cn", map[string]string{"X-AzurPilot-ID": testDeviceID})
		if rec.Code != http.StatusOK {
			t.Fatalf("期望 200: %d %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"maintenance":null`) {
			t.Fatalf("期望 maintenance:null: %s", rec.Body.String())
		}
	})
	t.Run("存在维护安排", func(t *testing.T) {
		ends := time.Date(2026, 10, 29, 7, 0, 0, 0, time.UTC)
		backend := &fakeBackend{maintenance: &store.Maintenance{
			Server: "cn", Name: "国服", StartsAt: time.Date(2026, 10, 29, 1, 0, 0, 0, time.UTC), EndsAt: &ends,
		}}
		srv, _ := newTestServer(t, backend, nil)
		rec := get(srv, "/api/v2/get/maintenance?server=cn", map[string]string{"X-AzurPilot-ID": testDeviceID})
		body := rec.Body.String()
		if rec.Code != http.StatusOK || !strings.Contains(body, `"name":"国服"`) ||
			!strings.Contains(body, `"starts_at":"2026-10-29T01:00:00Z"`) ||
			!strings.Contains(body, `"ends_at":"2026-10-29T07:00:00Z"`) {
			t.Fatalf("响应错误: %d %s", rec.Code, body)
		}
	})
	t.Run("数据源失败返回 503", func(t *testing.T) {
		backend := &fakeBackend{maintErr: errors.New("db down")}
		srv, _ := newTestServer(t, backend, nil)
		rec := get(srv, "/api/v2/get/maintenance?server=cn", map[string]string{"X-AzurPilot-ID": testDeviceID})
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "SERVICE_UNAVAILABLE") {
			t.Fatalf("期望 503 SERVICE_UNAVAILABLE: %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("server 非法返回 400", func(t *testing.T) {
		backend := &fakeBackend{}
		srv, _ := newTestServer(t, backend, nil)
		rec := get(srv, "/api/v2/get/maintenance?server=kr", map[string]string{"X-AzurPilot-ID": testDeviceID})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("期望 400: %d", rec.Code)
		}
	})
}

func TestNotFoundFallback(t *testing.T) {
	backend := &fakeBackend{}
	srv, _ := newTestServer(t, backend, nil)
	rec := get(srv, "/api/v2/unknown", map[string]string{"X-AzurPilot-ID": testDeviceID})
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"schema_version":2`) {
		t.Fatalf("期望统一 404 信封: %d %s", rec.Code, rec.Body.String())
	}
}

func TestHealthz(t *testing.T) {
	backend := &fakeBackend{}
	srv, _ := newTestServer(t, backend, nil)
	rec := get(srv, "/healthz", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Fatalf("healthz 错误: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRateLimit(t *testing.T) {
	// 直接测试限流器本身（服务默认配置很宽松）。
	lim := newRateLimiter(0.001, 1) // 每秒 0.001 个令牌，桶容量 1
	lim.Run(context.Background())
	t.Cleanup(lim.Close)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.RemoteAddr = "203.0.113.7:1234"

	if !lim.allow("203.0.113.7") {
		t.Fatal("首请求应当通过")
	}
	if lim.allow("203.0.113.7") {
		t.Fatal("第二请求应当被限流")
	}
	if !lim.allow("198.51.100.9") {
		t.Fatal("其他 IP 不应受影响")
	}
}
