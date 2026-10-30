package adminws

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestOriginAuthenticationAndChangedStateBroadcast(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var value atomic.Int64
	hub := New("secret", []string{"https://admin.azurpilot.com"}, func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		if method != "increment" {
			return nil, Fault{Code: "METHOD_NOT_FOUND", Message: "Unknown", Status: 404}
		}
		return value.Add(1), nil
	}, func(context.Context) (any, error) { return map[string]int64{"value": value.Load()}, nil })
	hub.Run(ctx)
	defer hub.Close()
	host := httptest.NewServer(hub)
	defer host.Close()
	url := "ws" + strings.TrimPrefix(host.URL, "http")
	_, response, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": []string{"https://foreign.example"}}})
	if err == nil || response.StatusCode != 403 {
		t.Fatal("foreign origin accepted", err)
	}
	dial := func(token string) *websocket.Conn {
		t.Helper()
		conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": []string{"https://admin.azurpilot.com"}}})
		if err != nil {
			t.Fatal(err)
		}
		if err := wsjson.Write(ctx, conn, map[string]string{"type": "auth", "token": token}); err != nil {
			t.Fatal(err)
		}
		return conn
	}
	wrong := dial("wrong")
	var auth map[string]any
	if err := wsjson.Read(ctx, wrong, &auth); err != nil || auth["type"] != "error" {
		t.Fatal(auth, err)
	}
	_ = wrong.CloseNow()
	conn := dial("secret")
	defer func() { _ = conn.CloseNow() }()
	if err := wsjson.Read(ctx, conn, &auth); err != nil || auth["type"] != "authenticated" {
		t.Fatal(auth, err)
	}
	if err := wsjson.Write(ctx, conn, map[string]string{"id": "sub", "method": "state.subscribe"}); err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := wsjson.Read(ctx, conn, &result); err != nil || result["id"] != "sub" {
		t.Fatal(result, err)
	}
	if err := wsjson.Write(ctx, conn, map[string]string{"id": "change", "method": "increment"}); err != nil {
		t.Fatal(err)
	}
	// A subscription update may arrive before the command response. They are
	// independent envelopes and clients correlate replies using the request ID.
	gotResponse, changed := false, false
	for !gotResponse || !changed {
		result = make(map[string]any)
		if err := wsjson.Read(ctx, conn, &result); err != nil {
			t.Fatal(err)
		}
		if result["id"] == "change" && result["type"] == "result" {
			gotResponse = true
		}
		if result["type"] == "event" {
			data := result["data"].(map[string]any)
			if data["value"] != float64(1) {
				t.Fatal(data)
			}
			changed = true
		}
	}
	hub.Notify()
	quiet, stop := context.WithTimeout(ctx, 150*time.Millisecond)
	defer stop()
	if err := wsjson.Read(quiet, conn, &result); err == nil {
		t.Fatal("unchanged state was sent again", result)
	}
}
