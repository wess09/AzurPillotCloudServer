package templateserver

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ap-cloud-server/internal/v3db"
)

func testTemplate(key string, icon []byte) v3db.Template {
	sum := sha256.Sum256(icon)
	h := hex.EncodeToString(sum[:])
	return v3db.Template{
		Key: key, Name: key, Item: key, Tag: "", SceneContext: "all",
		Kind: v3db.KindFormal, SHA256: h, IconSHA256: h, Icon: icon, Enabled: true,
	}
}

func testSigner(t *testing.T) *Signer {
	t.Helper()
	s, err := GenerateSigner()
	if err != nil {
		t.Fatalf("生成签名密钥失败: %v", err)
	}
	return s
}

func TestBuildReleaseSignsManifest(t *testing.T) {
	signer := testSigner(t)
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	a := testTemplate("a", patternPNG(t, 1))
	b := testTemplate("b", patternPNG(t, 2))

	plan, err := BuildRelease(3, []v3db.Template{b, a}, nil, nil, now, signer, 30)
	if err != nil {
		t.Fatalf("BuildRelease: %v", err)
	}
	if !Verify(signer.PublicKeyBase64(), plan.ManifestBytes, plan.Signature) {
		t.Fatal("manifest 签名无法验证")
	}
	if Verify(signer.PublicKeyBase64(), append(append([]byte(nil), plan.ManifestBytes...), ' '), plan.Signature) {
		t.Fatal("篡改后的 manifest 不应通过验签")
	}
	sum := sha256.Sum256(plan.ManifestBytes)
	if hex.EncodeToString(sum[:]) != plan.ManifestSHA256 {
		t.Fatal("manifest sha256 不一致")
	}
	var m Manifest
	if err := json.Unmarshal(plan.ManifestBytes, &m); err != nil {
		t.Fatalf("解析 manifest: %v", err)
	}
	if m.SchemaVersion != 3 || m.Revision != 3 || len(m.Entries) != 2 {
		t.Fatalf("manifest 内容异常: %+v", m)
	}
	if m.Entries[0].Key != "a" || m.Entries[1].Key != "b" {
		t.Fatalf("manifest 条目未按 key 排序: %+v", m.Entries)
	}
	icon, err := base64.StdEncoding.DecodeString(m.Entries[0].ImageBase64)
	if err != nil || string(icon) != string(a.Icon) {
		t.Fatal("manifest 内嵌图标与源图不一致")
	}
	if len(plan.Mappings) == 0 || !json.Valid(plan.Mappings) {
		t.Fatal("mappings JSON 无效")
	}
}

func TestWriteDistDeltaAndTombstones(t *testing.T) {
	signer := testSigner(t)
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	a1 := testTemplate("a", patternPNG(t, 1))
	b := testTemplate("b", patternPNG(t, 2))
	plan1, err := BuildRelease(1, []v3db.Template{a1, b}, nil, nil, now, signer, 30)
	if err != nil {
		t.Fatal(err)
	}
	dir1 := t.TempDir()
	if err := writeDist(dir1, plan1); err != nil {
		t.Fatalf("writeDist v1: %v", err)
	}
	var cat1 []CatalogEntry
	if err := json.Unmarshal(plan1.CatalogJSON, &cat1); err != nil {
		t.Fatal(err)
	}

	a2 := testTemplate("a", patternPNG(t, 4))
	c := testTemplate("c", patternPNG(t, 3))
	plan2, err := BuildRelease(2, []v3db.Template{a2, c}, map[uint64][]CatalogEntry{1: cat1}, nil, now, signer, 30)
	if err != nil {
		t.Fatal(err)
	}
	dir2 := t.TempDir()
	if err := writeDist(dir2, plan2); err != nil {
		t.Fatalf("writeDist v2: %v", err)
	}

	var full Delta
	readJSON(t, filepath.Join(dir2, "deltas", "0.json"), &full)
	if full.From != 0 || full.Revision != 2 || len(full.Entries) != 2 || len(full.Tombstones) != 0 {
		t.Fatalf("全量增量异常: %+v", full)
	}

	var delta Delta
	readJSON(t, filepath.Join(dir2, "deltas", "1.json"), &delta)
	if delta.Revision != 2 || delta.From != 1 {
		t.Fatalf("增量 revision/from 异常: %+v", delta)
	}
	keys := make(map[string]bool, len(delta.Entries))
	for _, e := range delta.Entries {
		keys[e.Key] = true
	}
	if !keys["a"] || !keys["c"] || keys["b"] {
		t.Fatalf("增量条目异常: %+v", keys)
	}
	if len(delta.Tombstones) != 1 || delta.Tombstones[0] != "b" {
		t.Fatalf("墓碑异常: %+v", delta.Tombstones)
	}

	for _, tpl := range plan2.Templates {
		sum := sha256.Sum256(tpl.Icon)
		name := hex.EncodeToString(sum[:]) + ".png"
		if _, err := os.Stat(filepath.Join(dir2, "icons", name)); err != nil {
			t.Fatalf("缺少图标 %s: %v", name, err)
		}
	}
}

func TestPublisherLocalMode(t *testing.T) {
	signer := testSigner(t)
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	plan, err := BuildRelease(1, []v3db.Template{testTemplate("a", patternPNG(t, 1))}, nil, nil, now, signer, 30)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	p := &Publisher{
		Dir: dir, PublicBase: "https://templates.example/", CFDeploy: false,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	url, err := p.Deploy(context.Background(), plan)
	if err != nil {
		t.Fatalf("本地发布失败: %v", err)
	}
	if url != "https://templates.example/api/v3/get/templates/manifest" {
		t.Fatalf("发布 URL 异常: %s", url)
	}
	body, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil || string(body) != string(plan.ManifestBytes) {
		t.Fatalf("manifest.json 未落盘: %v", err)
	}
	sig, err := os.ReadFile(filepath.Join(dir, "manifest.sig"))
	if err != nil || string(trimSpace(sig)) != plan.Signature {
		t.Fatalf("manifest.sig 未落盘: %v", err)
	}
}

func TestSignerRejectsWrongKey(t *testing.T) {
	a, b := testSigner(t), testSigner(t)
	msg := []byte("manifest")
	sig := a.Sign(msg)
	if !Verify(a.PublicKeyBase64(), msg, sig) {
		t.Fatal("正确公钥应验签成功")
	}
	if Verify(b.PublicKeyBase64(), msg, sig) {
		t.Fatal("错误公钥不应验签成功")
	}
	if _, err := NewSigner("not-a-key"); err == nil {
		t.Fatal("非法私钥应报错")
	}
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s: %v", path, err)
	}
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("解析 %s: %v", path, err)
	}
}

func trimSpace(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r' || b[len(b)-1] == ' ') {
		b = b[:len(b)-1]
	}
	return b
}
