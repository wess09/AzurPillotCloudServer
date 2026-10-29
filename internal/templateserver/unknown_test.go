package templateserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ap-cloud-server/internal/v3db"
)

func randomIcon(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 49, 49))
	buf := make([]byte, 49*49*4)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("生成随机图像失败: %v", err)
	}
	for y := 0; y < 49; y++ {
		for x := 0; x < 49; x++ {
			i := (y*49 + x) * 4
			img.SetRGBA(x, y, color.RGBA{R: buf[i], G: buf[i+1], B: buf[i+2], A: 255})
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatalf("编码随机图像失败: %v", err)
	}
	return out.Bytes()
}

func newTestServer(t *testing.T) (*Server, Config) {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("未设置 TEST_DATABASE_URL，跳过数据库集成测试")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := v3db.Open(ctx, dbURL)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	cfg := Config{
		ListenAddr: ":0", DatabaseURL: dbURL,
		AdminToken: "test-admin", IngestToken: "test-ingest",
		PublishDir: t.TempDir(), PublicBase: "https://templates.example",
		WorkerName: "test-worker", CFDeploy: false, SelfCheck: false,
		CheckAttempts: 1, CheckDelay: time.Millisecond, ExpiresDays: 30,
		ShutdownGrace: time.Second,
	}
	srv, err := NewServer(cfg, store.DB, testSigner(t), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("创建模板服务器失败: %v", err)
	}
	return srv, cfg
}

func uploadBody(t *testing.T, icon []byte) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"scene":        "commission",
		"image_base64": base64.StdEncoding.EncodeToString(icon),
		"client":       "test/1.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func postJSON(t *testing.T, h http.Handler, path, token string, body []byte) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s 返回非 JSON: %s", path, rec.Body.String())
	}
	return rec.Code, out
}

func cleanupUnknown(t *testing.T, srv *Server, keys *[]string) {
	t.Helper()
	t.Cleanup(func() {
		if len(*keys) == 0 {
			return
		}
		srv.db.Where("cluster_key IN ?", *keys).Delete(&v3db.UnknownTemplate{})
		srv.db.Where("cluster_key IN ?", *keys).Delete(&v3db.UnknownMapping{})
		srv.db.Where("from_key IN ?", *keys).Delete(&v3db.UnknownClusterAlias{})
		srv.db.Where("key IN ?", *keys).Delete(&v3db.Template{})
	})
}

func TestUnknownTemplateLifecycle(t *testing.T) {
	srv, cfg := newTestServer(t)
	h := srv.Handler()
	var keys []string
	cleanupUnknown(t, srv, &keys)

	// 1. 首次上传 → 注册新聚类。
	icon1 := randomIcon(t)
	code, out := postJSON(t, h, "/api/v3/post/templates/unknown", "test-ingest", uploadBody(t, icon1))
	if code != http.StatusOK || out["status"] != "registered" {
		t.Fatalf("首次上传失败: code=%d out=%v", code, out)
	}
	key1, _ := out["cluster_key"].(string)
	if !strings.HasPrefix(key1, "U-") {
		t.Fatalf("聚类 key 异常: %q", key1)
	}
	keys = append(keys, key1)

	// 2. 相同图标再次上传 → 命中同一聚类并累加计数。
	code, out = postJSON(t, h, "/api/v3/post/templates/unknown", "test-ingest", uploadBody(t, icon1))
	if code != http.StatusOK || out["status"] != "pending" {
		t.Fatalf("重复上传状态异常: code=%d out=%v", code, out)
	}
	if n, _ := out["occurrences"].(float64); n < 2 {
		t.Fatalf("聚类计数未累加: %v", out["occurrences"])
	}

	// 3. 管理端映射并注册为模板。
	code, out = postJSON(t, h, "/api/v1/templates/unknown/"+key1+"/map", "test-admin",
		[]byte(`{"item":"Coins","name":"金币","register":true}`))
	if code != http.StatusOK || out["status"] != "mapped" {
		t.Fatalf("映射失败: code=%d out=%v", code, out)
	}
	if rev, _ := out["revision"].(float64); rev < 1 {
		t.Fatalf("映射未分配 revision: %v", out)
	}

	// 4. 注册后同一图标会被已知模板直接命中。
	code, out = postJSON(t, h, "/api/v3/post/templates/unknown", "test-ingest", uploadBody(t, icon1))
	if code != http.StatusOK || out["status"] != "matched" || out["template_key"] != key1 {
		t.Fatalf("已注册模板未命中: code=%d out=%v", code, out)
	}

	// 5. 发布后 mappings.json 与 manifest 都包含该聚类/模板。
	code, out = postJSON(t, h, "/api/v1/templates/publish", "test-admin", []byte(`{"note":"test"}`))
	if code != http.StatusOK {
		t.Fatalf("发布失败: code=%d out=%v", code, out)
	}
	if out["deployed"] != false {
		t.Fatalf("本地模式不应标记为已部署: %v", out["deployed"])
	}
	raw, err := os.ReadFile(filepath.Join(cfg.PublishDir, "mappings.json"))
	if err != nil {
		t.Fatalf("读取 mappings.json: %v", err)
	}
	var mappings struct {
		Mappings []struct {
			ClusterKey string `json:"cluster_key"`
			Item       string `json:"item"`
		} `json:"mappings"`
	}
	if err := json.Unmarshal(raw, &mappings); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range mappings.Mappings {
		if m.ClusterKey == key1 && m.Item == "Coins" {
			found = true
		}
	}
	if !found {
		t.Fatalf("mappings.json 缺少聚类映射: %s", raw)
	}
	var full Delta
	readJSON(t, filepath.Join(cfg.PublishDir, "deltas", "0.json"), &full)
	found = false
	for _, e := range full.Entries {
		if e.Key == key1 {
			found = true
		}
	}
	if !found {
		t.Fatal("全量增量缺少已注册模板")
	}

	// 6. 合并聚类后，旧聚类的新上传会被重定向到目标。
	icon2 := randomIcon(t)
	code, out = postJSON(t, h, "/api/v3/post/templates/unknown", "test-ingest", uploadBody(t, icon2))
	if code != http.StatusOK || out["status"] != "registered" {
		t.Fatalf("第二聚类注册失败: code=%d out=%v", code, out)
	}
	key2, _ := out["cluster_key"].(string)
	keys = append(keys, key2)
	code, out = postJSON(t, h, "/api/v1/templates/unknown/merge", "test-admin",
		[]byte(`{"from_key":"`+key2+`","to_key":"`+key1+`"}`))
	if code != http.StatusOK {
		t.Fatalf("合并失败: code=%d out=%v", code, out)
	}
	code, out = postJSON(t, h, "/api/v3/post/templates/unknown", "test-ingest", uploadBody(t, icon2))
	if code != http.StatusOK || out["cluster_key"] != key1 {
		t.Fatalf("合并后的上传未重定向: code=%d out=%v", code, out)
	}

	// 7. 拒绝的聚类不再吸收上传。
	icon3 := randomIcon(t)
	code, out = postJSON(t, h, "/api/v3/post/templates/unknown", "test-ingest", uploadBody(t, icon3))
	if code != http.StatusOK {
		t.Fatalf("第三聚类注册失败: code=%d out=%v", code, out)
	}
	key3, _ := out["cluster_key"].(string)
	keys = append(keys, key3)
	code, out = postJSON(t, h, "/api/v1/templates/unknown/"+key3+"/reject", "test-admin", nil)
	if code != http.StatusOK {
		t.Fatalf("拒绝失败: code=%d out=%v", code, out)
	}
	code, out = postJSON(t, h, "/api/v3/post/templates/unknown", "test-ingest", uploadBody(t, icon3))
	if code != http.StatusOK || out["status"] != "rejected" {
		t.Fatalf("拒绝后的上传状态异常: code=%d out=%v", code, out)
	}
}

func TestUnknownUploadRequiresToken(t *testing.T) {
	srv, _ := newTestServer(t)
	code, out := postJSON(t, srv.Handler(), "/api/v3/post/templates/unknown", "wrong", uploadBody(t, randomIcon(t)))
	if code != http.StatusForbidden {
		t.Fatalf("错误凭据应被拒绝: code=%d out=%v", code, out)
	}
}
