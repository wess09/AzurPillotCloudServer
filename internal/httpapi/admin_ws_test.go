package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestManagementWebSocketSurvivesHTTPTimeoutAndStreamsState(t *testing.T) {
	server, _ := newTestServer(t, &fakeBackend{}, nil)
	// Test the complete response-writer and timeout middleware stack.
	httpServer := httptest.NewServer(timeoutMiddleware(10*time.Millisecond, 0, server.Handler()))
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	socket, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(httpServer.URL, "http")+"/api/v1/admin/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": []string{"https://admin.azurpilot.com"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = socket.CloseNow() }()
	if err := wsjson.Write(ctx, socket, map[string]any{"type": "auth", "token": "test-admin-token"}); err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := wsjson.Read(ctx, socket, &result); err != nil || result["type"] != "authenticated" {
		t.Fatalf("auth: %v %v", result, err)
	}
	time.Sleep(35 * time.Millisecond)
	if err := wsjson.Write(ctx, socket, map[string]any{"id": "1", "method": "state.subscribe", "params": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if err := wsjson.Read(ctx, socket, &result); err != nil || result["id"] != "1" || result["type"] != "result" {
		t.Fatalf("subscription response: %v %v", result, err)
	}
	if err := wsjson.Write(ctx, socket, map[string]any{"id": "2", "method": "update_policy.set", "params": map[string]any{"force_update": true}}); err != nil {
		t.Fatal(err)
	}
	response, event := false, false
	for !response || !event {
		result = make(map[string]any)
		if err := wsjson.Read(ctx, socket, &result); err != nil {
			t.Fatal(err)
		}
		if result["id"] == "2" {
			response = true
		}
		if result["type"] == "event" {
			state := result["data"].(map[string]any)
			event = state["force_update"] == true
		}
	}
}

func TestPublicCORSIsLimitedToStatistics(t *testing.T) {
	server, _ := newTestServer(t, &fakeBackend{}, nil)
	for _, test := range []struct {
		path, origin string
		status       int
	}{
		{"/api/v3/get/research", "https://stats.azurpilot.com", 204},
		{"/api/v3/get/analysis", "https://stats.azurpilot.com", 204},
		{"/api/v3/get/analysis", "https://other.example", 403},
		{"/api/v3/get/research", "https://other.example", 403},
		{"/api/v2/get/research", "https://stats.azurpilot.com", 404},
		{"/api/upload", "https://stats.azurpilot.com", 404},
		{"/api/v1/admin/statistics/overview", "https://stats.azurpilot.com", 404},
	} {
		request := httptest.NewRequest("OPTIONS", test.path, nil)
		request.Header.Set("Origin", test.origin)
		request.Header.Set("Access-Control-Request-Method", "GET")
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != test.status {
			t.Errorf("%s %s: %d", test.path, test.origin, response.Code)
		}
	}
}
