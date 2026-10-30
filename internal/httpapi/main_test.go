package httpapi

import (
	"context"
	"sync"
	"testing"
	"time"

	"ap-cloud-server/internal/config"
	"ap-cloud-server/internal/publish"
	"ap-cloud-server/internal/statuscache"
	"ap-cloud-server/internal/store"

	"go.uber.org/goleak"
)

// TestMain 在全部测试结束后检查协程泄露。
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// fakeBackend 是可编程的存储假实现。
type fakeBackend struct {
	mu            sync.Mutex
	outcome       store.Cl1Outcome
	payload       []byte
	submitErr     error
	submitted     *store.Cl1Record
	submittedHash string

	maintenance *store.Maintenance
	maintErr    error

	announcement *store.Announcement
	force        bool
	upsertErr    error
}

func (f *fakeBackend) SubmitCl1(ctx context.Context, rec store.Cl1Record, bodyHash string, responseJSON []byte) (store.Cl1Outcome, []byte, error) {
	f.submitted = &rec
	f.submittedHash = bodyHash
	if f.submitErr != nil {
		return 0, nil, f.submitErr
	}
	return f.outcome, f.payload, nil
}

func (f *fakeBackend) LatestMaintenance(ctx context.Context, server string) (*store.Maintenance, error) {
	return f.maintenance, f.maintErr
}

func (f *fakeBackend) UpsertMaintenance(ctx context.Context, m store.Maintenance) error {
	return f.upsertErr
}
func (f *fakeBackend) DeleteMaintenance(ctx context.Context, server string) (bool, error) {
	return true, nil
}
func (f *fakeBackend) ListMaintenances(ctx context.Context) ([]store.Maintenance, error) {
	return nil, nil
}

func (f *fakeBackend) LatestAnnouncement(ctx context.Context) (*store.Announcement, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.announcement, nil
}

func (f *fakeBackend) GenerateAnnouncementID(ctx context.Context, now time.Time) (string, error) {
	return "notice-20261026-001", nil
}

func (f *fakeBackend) SetAnnouncement(ctx context.Context, a store.Announcement) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.announcement = &a
	return nil
}
func (f *fakeBackend) WithdrawAnnouncement(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.announcement = nil
	return nil
}
func (f *fakeBackend) ListAnnouncements(ctx context.Context, limit int) ([]store.Announcement, error) {
	return nil, nil
}
func (f *fakeBackend) GetUpdatePolicy(ctx context.Context) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.force, nil
}
func (f *fakeBackend) SetUpdatePolicy(ctx context.Context, force bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.force = force
	return nil
}
func (f *fakeBackend) RecordPublish(ctx context.Context, rec store.PublishRecord) error { return nil }
func (f *fakeBackend) LatestPublishByWorker(ctx context.Context) (map[string]store.PublishRecord, error) {
	return map[string]store.PublishRecord{}, nil
}

// newTestServer 构造被测服务：假存储、可编程缓存、真实发布器（无 CF 凭据）。
func newTestServer(t *testing.T, backend *fakeBackend, query statuscache.QueryFunc) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Config{
		ListenAddr:           "127.0.0.1:0",
		AdminToken:           "test-admin-token",
		AdminOrigins:         []string{"https://admin.azurpilot.com"},
		PublicOrigins:        []string{"https://stats.azurpilot.com"},
		RequestTimeout:       5 * time.Second,
		RateLimitRPS:         1000,
		RateLimitBurst:       1000,
		PublishDir:           dir,
		PublishTimeout:       5 * time.Second,
		CacheTTL:             time.Second,
		CacheRefreshInterval: time.Hour,
		GatewayTimeout:       time.Second,
		Cloudflare: config.Cloudflare{
			NoticeWorker: publish.NoticeWorker,
			UpdateWorker: publish.UpdateWorker,
		},
	}
	cache := statuscache.New(time.Second, time.Hour, time.Second, query, nil)
	cache.Run(context.Background())
	t.Cleanup(cache.Close)

	publisher := publish.New(nil, nil, dir, nil)
	publisher.Run(context.Background())
	t.Cleanup(publisher.Close)

	deps := Deps{
		Cfg:   cfg,
		Store: backend,
		Cache: cache,
		Pub:   publisher,
		Now:   func() time.Time { return time.Date(2026, 10, 26, 10, 0, 1, 0, time.UTC) },
	}
	srv := New(deps)
	srv.Run(context.Background())
	t.Cleanup(srv.Close)
	return srv, dir
}

const testDeviceID = "0123456789abcdef0123456789abcdef"
