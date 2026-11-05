package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ap-cloud-server/internal/store"
)

// postCl1 构造 CL1 请求。
func postCl1(s *Server, body string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v2/post/cl1", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-AzurPilot-ID", testDeviceID)
	req.Header.Set("Cloudflare-Workers-Version-Key", testDeviceID)
	if mutate != nil {
		mutate(req)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

const validCl1Body = `{
  "request_id": "9b53d5a1-7bb6-4fc2-97c7-c602af093584",
  "device_id": "0123456789abcdef0123456789abcdef",
  "instance_id": "0123456789abcdef",
  "month": "2026-10",
  "collected_at": "2026-10-26T10:00:00.000Z",
  "battle_count": 1200,
  "akashi_encounters": 60,
  "akashi_ap": 4200
}`

func TestCl1Accepted(t *testing.T) {
	backend := &fakeBackend{outcome: store.Cl1Accepted}
	srv, _ := newTestServer(t, backend, nil)

	rec := postCl1(srv, validCl1Body, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，得到 %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`"schema_version":2`,
		`"accepted":true`,
		`"duplicate":false`,
		`"received_at":"2026-10-26T10:00:01Z"`,
		`"battle_rounds":600`,
		`"sortie_cost":72000`,
		`"akashi_probability":0.1`,
		`"average_stamina":70`,
		`"net_stamina_gain":4200`,
		`"collected_at":"2026-10-26T10:00:00.000Z"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("响应缺少 %s: %s", want, body)
		}
	}
	if backend.submitted == nil || backend.submitted.BattleCount != 1200 {
		t.Fatalf("存储收到的记录错误: %+v", backend.submitted)
	}
	if len(backend.submittedHash) != 64 {
		t.Fatalf("正文哈希应为 SHA-256 十六进制: %q", backend.submittedHash)
	}
}

func TestCl1Validation(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		mutate func(*http.Request)
		want   int
		code   string
	}{
		{"未知字段", `{"request_id":"9b53d5a1-7bb6-4fc2-97c7-c602af093584","device_id":"0123456789abcdef0123456789abcdef","instance_id":"0123456789abcdef","month":"2026-10","collected_at":"2026-10-26T10:00:00Z","battle_count":1,"akashi_encounters":0,"akashi_ap":0,"extra":1}`, nil, 400, "VALIDATION_ERROR"},
		{"布尔冒充整数", `{"request_id":"9b53d5a1-7bb6-4fc2-97c7-c602af093584","device_id":"0123456789abcdef0123456789abcdef","instance_id":"0123456789abcdef","month":"2026-10","collected_at":"2026-10-26T10:00:00Z","battle_count":true,"akashi_encounters":0,"akashi_ap":0}`, nil, 400, "VALIDATION_ERROR"},
		{"非法 JSON", `{not json`, nil, 400, "INVALID_JSON"},
		{"尾随内容", validCl1Body + `{"x":1}`, nil, 400, "INVALID_JSON"},
		{"request_id 非 UUID", strings.Replace(validCl1Body, "9b53d5a1-7bb6-4fc2-97c7-c602af093584", "not-a-uuid", 1), nil, 400, "VALIDATION_ERROR"},
		{"device_id 非法", strings.Replace(validCl1Body, `"0123456789abcdef0123456789abcdef",
  "instance_id`, `"xyz",
  "instance_id`, 1), nil, 400, "INVALID_DEVICE_ID"},
		{"设备 ID 不一致", validCl1Body, func(r *http.Request) {
			r.Header.Set("X-AzurPilot-ID", "ffffffffffffffffffffffffffffffff")
		}, 400, "DEVICE_ID_MISMATCH"},
		{"CF 亲和键不一致", validCl1Body, func(r *http.Request) {
			r.Header.Set("Cloudflare-Workers-Version-Key", "ffffffffffffffffffffffffffffffff")
		}, 400, "DEVICE_ID_MISMATCH"},
		{"月份非法", strings.Replace(validCl1Body, "2026-10", "2026-13", 1), nil, 400, "VALIDATION_ERROR"},
		{"时间缺时区", strings.Replace(validCl1Body, "2026-10-26T10:00:00.000Z", "2026-10-26T10:00:00", 1), nil, 400, "VALIDATION_ERROR"},
		{"缺少设备头", validCl1Body, func(r *http.Request) {
			r.Header.Set("X-AzurPilot-ID", "")
		}, 400, "INVALID_DEVICE_ID"},
		{"错误方法", "", func(r *http.Request) {
			r.Method = http.MethodGet
		}, 405, "METHOD_NOT_ALLOWED"},
		{"错误 Content-Type", validCl1Body, func(r *http.Request) {
			r.Header.Set("Content-Type", "text/plain")
		}, 415, "UNSUPPORTED_MEDIA_TYPE"},
		{"超 64KiB", fmt.Sprintf(`{"request_id":"9b53d5a1-7bb6-4fc2-97c7-c602af093584","device_id":"0123456789abcdef0123456789abcdef","instance_id":"0123456789abcdef","month":"2026-10","collected_at":"2026-10-26T10:00:00Z","battle_count":1,"akashi_encounters":0,"akashi_ap":0,"pad":"%s"}`, strings.Repeat("a", 70<<10)), nil, 413, "PAYLOAD_TOO_LARGE"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend := &fakeBackend{}
			srv, _ := newTestServer(t, backend, nil)
			rec := postCl1(srv, tc.body, tc.mutate)
			if rec.Code != tc.want {
				t.Fatalf("期望 %d，得到 %d: %s", tc.want, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), `"code":"`+tc.code+`"`) {
				t.Fatalf("期望错误码 %s: %s", tc.code, rec.Body.String())
			}
		})
	}
}

func TestCl1DuplicateAndConflicts(t *testing.T) {
	t.Run("重复请求返回原结果并标记 duplicate", func(t *testing.T) {
		backend := &fakeBackend{
			outcome: store.Cl1Duplicate,
			payload: []byte(`{"schema_version":2,"request_id":"9b53d5a1-7bb6-4fc2-97c7-c602af093584","accepted":true,"duplicate":false,"received_at":"2026-10-26T10:00:01Z","snapshot":{}}`),
		}
		srv, _ := newTestServer(t, backend, nil)
		rec := postCl1(srv, validCl1Body, nil)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"duplicate":true`) {
			t.Fatalf("期望 duplicate:true: %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("同 ID 不同正文返回 409", func(t *testing.T) {
		backend := &fakeBackend{outcome: store.Cl1RequestConflict}
		srv, _ := newTestServer(t, backend, nil)
		rec := postCl1(srv, validCl1Body, nil)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "REQUEST_CONFLICT") {
			t.Fatalf("期望 409 REQUEST_CONFLICT: %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("旧快照返回 409", func(t *testing.T) {
		backend := &fakeBackend{outcome: store.Cl1StaleSnapshot}
		srv, _ := newTestServer(t, backend, nil)
		rec := postCl1(srv, validCl1Body, nil)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "STALE_SNAPSHOT") {
			t.Fatalf("期望 409 STALE_SNAPSHOT: %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("存储错误返回 500", func(t *testing.T) {
		backend := &fakeBackend{submitErr: fmt.Errorf("db down")}
		srv, _ := newTestServer(t, backend, nil)
		rec := postCl1(srv, validCl1Body, nil)
		if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "INTERNAL_ERROR") {
			t.Fatalf("期望 500 INTERNAL_ERROR: %d %s", rec.Code, rec.Body.String())
		}
	})
}

func TestComputeMetrics(t *testing.T) {
	cases := []struct {
		battles, encounters, ap int64
		rounds, cost, gain      int64
		prob, avg               float64
	}{
		{1200, 60, 4200, 600, 72000, 4200, 0.1, 70},
		{1, 0, 0, 0, 0, 0, 0, 0},           // 单场战斗：battle_rounds=0，除数为零结果为 0
		{32, 1, 7, 16, 1920, 7, 0.0625, 7}, // 1/16 无舍入
		{64, 1, 3, 32, 3840, 3, 0.0312, 3}, // 1/32 → 0.03125 → 与 Python round 一致进到偶数 0.0312
	}
	for i, tc := range cases {
		rounds, cost, gain, prob, avg := computeMetrics(tc.battles, tc.encounters, tc.ap)
		if rounds != tc.rounds || cost != tc.cost || gain != tc.gain || prob != tc.prob || avg != tc.avg {
			t.Fatalf("case %d: got (%d,%d,%d,%v,%v) want (%d,%d,%d,%v,%v)",
				i, rounds, cost, gain, prob, avg, tc.rounds, tc.cost, tc.gain, tc.prob, tc.avg)
		}
	}
}

func TestRoundFloatMatchesPythonRound(t *testing.T) {
	// Python: round(0.03125, 4) == 0.0312（银行家舍入）。
	if got := roundFloat(0.03125, 4); got != 0.0312 {
		t.Fatalf("round(0.03125, 4) = %v，期望 0.0312", got)
	}
	if got := roundFloat(0.035, 2); got != 0.04 {
		t.Fatalf("round(0.035, 2) = %v，期望 0.04", got)
	}
	if got := roundFloat(70.0, 2); got != 70 {
		t.Fatalf("round(70, 2) = %v，期望 70", got)
	}
}
