package templateserver

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ap-cloud-server/internal/v3db"
)

//go:embed assets/worker.js
var workerJS []byte

type ManifestEntry struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Item        string `json:"item"`
	Tag         string `json:"tag"`
	Scene       string `json:"scene"`
	Kind        string `json:"kind"`
	SHA256      string `json:"sha256"`
	IconSHA256  string `json:"icon_sha256"`
	ImageBase64 string `json:"image_base64,omitempty"`
}

type CatalogEntry struct {
	Key        string `json:"key"`
	Name       string `json:"name"`
	Item       string `json:"item"`
	Tag        string `json:"tag"`
	Scene      string `json:"scene"`
	Kind       string `json:"kind"`
	SHA256     string `json:"sha256"`
	IconSHA256 string `json:"icon_sha256"`
}

type Manifest struct {
	SchemaVersion int             `json:"schema_version"`
	Revision      uint64          `json:"revision"`
	Scenes        []string        `json:"scenes"`
	Expires       string          `json:"expires"`
	Entries       []ManifestEntry `json:"entries"`
}

type Delta struct {
	SchemaVersion int             `json:"schema_version"`
	Revision      uint64          `json:"revision"`
	From          uint64          `json:"from"`
	Entries       []ManifestEntry `json:"entries"`
	Tombstones    []string        `json:"tombstones"`
}

type ReleasePlan struct {
	Revision       uint64
	ManifestBytes  []byte
	ManifestSHA256 string
	Signature      string
	CatalogJSON    []byte
	Templates      []v3db.Template
	PrevCatalogs   map[uint64][]CatalogEntry
	Mappings       []byte
}

// BuildRelease 生成 manifest、签名与增量文件所需的数据。
func BuildRelease(revision uint64, templates []v3db.Template, prev map[uint64][]CatalogEntry, mappings []byte, now time.Time, signer *Signer, expiresDays int) (*ReleasePlan, error) {
	if expiresDays <= 0 {
		expiresDays = 30
	}
	sorted := append([]v3db.Template(nil), templates...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Key < sorted[j].Key })

	entries := make([]ManifestEntry, 0, len(sorted))
	catalog := make([]CatalogEntry, 0, len(sorted))
	for _, t := range sorted {
		sha := sha256.Sum256(t.Icon)
		sum := hex.EncodeToString(sha[:])
		entry := ManifestEntry{
			Key: t.Key, Name: t.Name, Item: t.Item, Tag: t.Tag,
			Scene: t.SceneContext, Kind: t.Kind,
			SHA256: sum, IconSHA256: sum,
			ImageBase64: base64.StdEncoding.EncodeToString(t.Icon),
		}
		entries = append(entries, entry)
		catalog = append(catalog, CatalogEntry{
			Key: t.Key, Name: t.Name, Item: t.Item, Tag: t.Tag,
			Scene: t.SceneContext, Kind: t.Kind,
			SHA256: sum, IconSHA256: sum,
		})
	}
	manifest := Manifest{
		SchemaVersion: 3,
		Revision:      revision,
		Scenes:        []string{v3db.SceneCommission, v3db.SceneResearch},
		Expires:       now.Add(time.Duration(expiresDays) * 24 * time.Hour).UTC().Format(time.RFC3339),
		Entries:       entries,
	}
	body, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	sha := sha256.Sum256(body)
	catalogJSON, err := json.Marshal(catalog)
	if err != nil {
		return nil, err
	}
	if len(mappings) == 0 {
		mappings = []byte(fmt.Sprintf(`{"schema_version":3,"revision":%d,"mappings":[],"aliases":[]}`, revision))
	}
	return &ReleasePlan{
		Revision:       revision,
		ManifestBytes:  body,
		ManifestSHA256: hex.EncodeToString(sha[:]),
		Signature:      signer.Sign(body),
		CatalogJSON:    catalogJSON,
		Templates:      sorted,
		PrevCatalogs:   prev,
		Mappings:       mappings,
	}, nil
}

// Publisher 负责把 ReleasePlan 写成静态目录并部署到 Cloudflare。
type Publisher struct {
	Dir           string
	PublicBase    string
	WorkerName    string
	WranglerImage string
	CFDeploy      bool
	AccountID     string
	APIToken      string
	SelfCheck     bool
	CheckAttempts int
	CheckDelay    time.Duration
	Logger        *slog.Logger
	runCommand    func(ctx context.Context, env []string, name string, args ...string) ([]byte, error)
}

func (p *Publisher) Deploy(ctx context.Context, plan *ReleasePlan) (string, error) {
	if p.Logger == nil {
		p.Logger = slog.Default()
	}
	if p.CFDeploy {
		return p.deployCloudflare(ctx, plan)
	}
	if err := writeDist(p.Dir, plan); err != nil {
		return "", err
	}
	return strings.TrimRight(p.PublicBase, "/") + "/api/v3/get/templates/manifest", nil
}

func (p *Publisher) deployCloudflare(ctx context.Context, plan *ReleasePlan) (string, error) {
	if strings.TrimSpace(p.AccountID) == "" || strings.TrimSpace(p.APIToken) == "" {
		return "", fmt.Errorf("CF_DEPLOY=true 但缺少 CLOUDFLARE_ACCOUNT_ID / CLOUDFLARE_API_TOKEN")
	}
	host := publicHost(p.PublicBase)
	if host == "" {
		return "", fmt.Errorf("CF_DEPLOY=true 但 TEMPLATE_PUBLIC_BASE 不是有效 URL")
	}
	work, err := os.MkdirTemp("", "azurpilot-templates-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(work)

	dist := filepath.Join(work, "dist")
	if err := writeDist(dist, plan); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(work, "worker.js"), workerJS, 0o644); err != nil {
		return "", err
	}
	config := fmt.Sprintf(`{
  "name": %q,
  "main": "worker.js",
  "compatibility_date": "2026-10-01",
  "assets": { "directory": "./dist", "binding": "ASSETS", "run_worker_first": true },
  "routes": [ { "pattern": %q, "custom_domain": true } ],
  "observability": { "enabled": true }
}
`, p.WorkerName, host)
	if err := os.WriteFile(filepath.Join(work, "wrangler.jsonc"), []byte(config), 0o644); err != nil {
		return "", err
	}

	env := append(os.Environ(),
		"CLOUDFLARE_API_TOKEN="+p.APIToken,
		"CLOUDFLARE_ACCOUNT_ID="+p.AccountID,
	)
	bin := "docker"
	args := []string{
		"run", "--rm",
		"-e", "CLOUDFLARE_API_TOKEN",
		"-e", "CLOUDFLARE_ACCOUNT_ID",
		"-v", work + ":/app",
		"-w", "/app",
		p.WranglerImage,
		"wrangler", "deploy",
	}
	// 镜像名为空时直接调用 PATH 中的 wrangler（模板服务器容器内置 Node + Wrangler）。
	if strings.TrimSpace(p.WranglerImage) == "" {
		bin = "wrangler"
		args = []string{"deploy"}
	}
	run := p.runCommand
	if run == nil {
		run = runExternal
	}
	out, err := run(ctx, env, bin, args...)
	if err != nil {
		return "", fmt.Errorf("wrangler deploy 失败: %w: %s", err, strings.TrimSpace(string(out)))
	}
	p.Logger.Info("模板 Worker 已部署", "revision", plan.Revision, "output", strings.TrimSpace(string(out)))

	if p.SelfCheck {
		url := strings.TrimRight(p.PublicBase, "/") + "/api/v3/get/templates/manifest"
		if err := selfCheck(ctx, url, plan.ManifestSHA256, p.CheckAttempts, p.CheckDelay); err != nil {
			return "", err
		}
	}
	// 本地保留一份镜像，供模板服务器自身与主 API 在上游异常时回退。
	if err := writeDist(p.Dir, plan); err != nil {
		p.Logger.Warn("写入本地模板镜像失败", "dir", p.Dir, "err", err)
	}
	return strings.TrimRight(p.PublicBase, "/") + "/api/v3/get/templates/manifest", nil
}

func writeDist(dir string, plan *ReleasePlan) error {
	if err := os.MkdirAll(filepath.Join(dir, "icons"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "deltas"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), plan.ManifestBytes, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.sig"), []byte(plan.Signature+"\n"), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.sha256"), []byte(plan.ManifestSHA256+"\n"), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "mappings.json"), plan.Mappings, 0o644); err != nil {
		return err
	}
	for _, t := range plan.Templates {
		sum := sha256.Sum256(t.Icon)
		name := hex.EncodeToString(sum[:]) + ".png"
		if err := os.WriteFile(filepath.Join(dir, "icons", name), t.Icon, 0o644); err != nil {
			return err
		}
	}
	current := make(map[string]ManifestEntry, len(plan.Templates))
	for _, t := range plan.Templates {
		sum := sha256.Sum256(t.Icon)
		key := hex.EncodeToString(sum[:])
		current[t.Key] = ManifestEntry{
			Key: t.Key, Name: t.Name, Item: t.Item, Tag: t.Tag,
			Scene: t.SceneContext, Kind: t.Kind,
			SHA256: key, IconSHA256: key,
			ImageBase64: base64.StdEncoding.EncodeToString(t.Icon),
		}
	}
	// from=0 永远返回全量，供过旧或未知客户端回退。
	full := Delta{SchemaVersion: 3, Revision: plan.Revision, From: 0, Entries: sortedEntries(current), Tombstones: []string{}}
	if err := writeDelta(dir, 0, full); err != nil {
		return err
	}
	for from, prev := range plan.PrevCatalogs {
		prevMap := make(map[string]CatalogEntry, len(prev))
		for _, e := range prev {
			prevMap[e.Key] = e
		}
		var changed []ManifestEntry
		for key, entry := range current {
			old, ok := prevMap[key]
			if !ok || old.IconSHA256 != entry.IconSHA256 || old.SHA256 != entry.SHA256 {
				changed = append(changed, entry)
			}
		}
		var tombstones []string
		for key := range prevMap {
			if _, ok := current[key]; !ok {
				tombstones = append(tombstones, key)
			}
		}
		sort.Strings(tombstones)
		sort.Slice(changed, func(i, j int) bool { return changed[i].Key < changed[j].Key })
		delta := Delta{SchemaVersion: 3, Revision: plan.Revision, From: from, Entries: changed, Tombstones: tombstones}
		if err := writeDelta(dir, from, delta); err != nil {
			return err
		}
	}
	return nil
}

func writeDelta(dir string, from uint64, delta Delta) error {
	body, err := json.Marshal(delta)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "deltas", fmt.Sprintf("%d.json", from)), body, 0o644)
}

func sortedEntries(m map[string]ManifestEntry) []ManifestEntry {
	out := make([]ManifestEntry, 0, len(m))
	for _, e := range m {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func publicHost(base string) string {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Host
}

func runExternal(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	return cmd.CombinedOutput()
}

var httpClient = &http.Client{Timeout: 15 * time.Second}

func httpRequest(ctx context.Context, url string) (*http.Request, error) {
	return http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
}

func selfCheck(ctx context.Context, url, want string, attempts int, delay time.Duration) error {
	if attempts <= 0 {
		attempts = 20
	}
	if delay <= 0 {
		delay = 5 * time.Second
	}
	var last error
	for i := 0; i < attempts; i++ {
		req, err := httpRequest(ctx, url)
		if err != nil {
			return err
		}
		resp, err := httpClient.Do(req)
		if err == nil {
			got := resp.Header.Get("X-Manifest-SHA256")
			resp.Body.Close()
			if resp.StatusCode == 200 && got == want {
				return nil
			}
			last = fmt.Errorf("manifest 自检不一致: status=%d sha=%q want=%q", resp.StatusCode, got, want)
		} else {
			last = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	if last == nil {
		last = fmt.Errorf("manifest 自检超时")
	}
	return fmt.Errorf("模板 Worker 自检失败: %w", last)
}
