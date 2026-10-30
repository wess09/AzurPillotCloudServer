package store

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// TestMain 检查协程泄露（连接池协程随 Close 全部退出）。
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// openTestStore 打开集成测试数据库；未设置 TEST_DATABASE_URL 时跳过。
// 本测试运行真实 PostgreSQL，CI 中由 service 容器提供。
func openTestStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("未设置 TEST_DATABASE_URL，跳过数据库集成测试")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := Open(ctx, url, 24*time.Hour)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func uniqueID(prefix string) string {
	return prefix + "-" + time.Now().Format("20060102150405.000000000")
}

func TestCl1SubmitLifecycle(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	requestID := uniqueID("req")
	device := uniqueID("dev")
	body1 := []byte(`{"schema_version":2,"accepted":true,"duplicate":false}`)
	hash1 := "aaaa"
	body2 := []byte(`{"schema_version":2,"accepted":true,"duplicate":false,"v":2}`)
	hash2 := "bbbb"

	rec := func(collected time.Time) Cl1Record {
		return Cl1Record{
			RequestID: requestID, DeviceID: device, InstanceID: "0123456789abcdef",
			Month: "2026-10", CollectedAt: collected,
			BattleCount: 1200, BattleRounds: 600, SortieCost: 72000,
			AkashiEncounters: 60, AkashiAP: 4200,
			AkashiProbability: 0.1, AverageStamina: 70, NetStaminaGain: 4200,
		}
	}

	// 1. 首次提交成功。
	outcome, payload, err := s.SubmitCl1(ctx, rec(now), hash1, body1)
	if err != nil || outcome != Cl1Accepted || string(payload) != string(body1) {
		t.Fatalf("首次提交错误: outcome=%v err=%v payload=%s", outcome, err, payload)
	}

	// 2. 同 ID 同哈希重试 → duplicate，返回原业务结果（JSONB 规范化键序后语义相同）。
	outcome, payload, err = s.SubmitCl1(ctx, rec(now.Add(time.Second)), hash1, body1)
	if err != nil || outcome != Cl1Duplicate || !jsonEqual(payload, body1) {
		t.Fatalf("重复提交错误: outcome=%v err=%v payload=%s", outcome, err, payload)
	}

	// 3. 同 ID 不同正文 → REQUEST_CONFLICT。
	outcome, _, err = s.SubmitCl1(ctx, rec(now.Add(2*time.Second)), hash2, body2)
	if err != nil || outcome != Cl1RequestConflict {
		t.Fatalf("冲突检测错误: outcome=%v err=%v", outcome, err)
	}

	// 4. 另一个 request_id 提交更旧的 collected_at → STALE_SNAPSHOT。
	stale := rec(now.Add(-time.Hour))
	stale.RequestID = uniqueID("req")
	outcome, _, err = s.SubmitCl1(ctx, stale, "cccc", body2)
	if err != nil || outcome != Cl1StaleSnapshot {
		t.Fatalf("旧快照检测错误: outcome=%v err=%v", outcome, err)
	}

	// 5. 更新的 collected_at 覆盖成功。
	fresh := rec(now.Add(time.Hour))
	fresh.RequestID = uniqueID("req")
	outcome, _, err = s.SubmitCl1(ctx, fresh, "dddd", body2)
	if err != nil || outcome != Cl1Accepted {
		t.Fatalf("覆盖更新错误: outcome=%v err=%v", outcome, err)
	}

	rows, err := s.ListCl1Snapshots(ctx, device, "2026-10", 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("快照数量错误: %d err=%v", len(rows), err)
	}
	if rows[0].CollectedAt.Before(now) {
		t.Fatalf("快照应为最新: %v", rows[0].CollectedAt)
	}
}

func TestCl1ConcurrentSameRequestID(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	requestID := uniqueID("conc")
	rec := Cl1Record{
		RequestID: requestID, DeviceID: uniqueID("dev"), InstanceID: "0123456789abcdef",
		Month: "2026-10", CollectedAt: now,
		BattleCount: 2, BattleRounds: 1, SortieCost: 120,
		AkashiEncounters: 1, AkashiAP: 5,
		AkashiProbability: 1, AverageStamina: 5, NetStaminaGain: 5,
	}
	body := []byte(`{}`)

	var wg sync.WaitGroup
	results := make([]Cl1Outcome, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			outcome, _, err := s.SubmitCl1(ctx, rec, "same-hash", body)
			if err != nil {
				t.Errorf("并发提交失败: %v", err)
				return
			}
			results[i] = outcome
		}(i)
	}
	wg.Wait()

	accepted, duplicated := 0, 0
	for _, o := range results {
		switch o {
		case Cl1Accepted:
			accepted++
		case Cl1Duplicate:
			duplicated++
		default:
			t.Fatalf("并发同 ID 同正文不应出现 %v", o)
		}
	}
	if accepted != 1 {
		t.Fatalf("恰好一次 Accepted，实际 %d", accepted)
	}
}

func TestMaintenanceLifecycle(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	starts := time.Now().UTC().Add(time.Hour)
	ends := starts.Add(6 * time.Hour)
	m := Maintenance{Server: "cn", Name: "国服", StartsAt: starts, EndsAt: &ends}
	if err := s.UpsertMaintenance(ctx, m); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	got, err := s.LatestMaintenance(ctx, "cn")
	if err != nil || got == nil || got.Name != "国服" {
		t.Fatalf("查询错误: %+v err=%v", got, err)
	}

	// ends_at 在过去 → 不再返回。
	past := Maintenance{Server: "cn", Name: "旧维护", StartsAt: time.Now().UTC().Add(-2 * time.Hour),
		EndsAt: ptrTime(time.Now().UTC().Add(-time.Hour))}
	if err := s.UpsertMaintenance(ctx, past); err != nil {
		t.Fatal(err)
	}
	got, err = s.LatestMaintenance(ctx, "cn")
	if err != nil || got != nil {
		t.Fatalf("已结束的维护不应返回: %+v err=%v", got, err)
	}

	// ends_at 为空 → 持续有效。
	open1 := Maintenance{Server: "jp", Name: "日服维护", StartsAt: time.Now().UTC()}
	if err := s.UpsertMaintenance(ctx, open1); err != nil {
		t.Fatal(err)
	}
	got, err = s.LatestMaintenance(ctx, "jp")
	if err != nil || got == nil || got.EndsAt != nil {
		t.Fatalf("ends_at 为空应持续有效: %+v err=%v", got, err)
	}

	deleted, err := s.DeleteMaintenance(ctx, "jp")
	if err != nil || !deleted {
		t.Fatalf("删除失败: %v err=%v", deleted, err)
	}
	got, err = s.LatestMaintenance(ctx, "jp")
	if err != nil || got != nil {
		t.Fatalf("删除后不应返回: %+v err=%v", got, err)
	}
}

func TestAnnouncementAndPolicy(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	id1, err := s.GenerateAnnouncementID(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	content := "内容"
	a1 := Announcement{ID: id1, Title: "公告一", Content: &content}
	if err := s.SetAnnouncement(ctx, a1); err != nil {
		t.Fatal(err)
	}
	got, err := s.LatestAnnouncement(ctx)
	if err != nil || got == nil || got.ID != id1 {
		t.Fatalf("查询错误: %+v err=%v", got, err)
	}

	// 新公告自动撤旧。
	id2, _ := s.GenerateAnnouncementID(ctx, time.Now())
	a2 := Announcement{ID: id2, Title: "公告二", URL: ptrString("https://example.com")}
	if err := s.SetAnnouncement(ctx, a2); err != nil {
		t.Fatal(err)
	}
	got, _ = s.LatestAnnouncement(ctx)
	if got.ID != id2 {
		t.Fatalf("应返回新公告: %+v", got)
	}
	history, _ := s.ListAnnouncements(ctx, 100)
	// 只统计本次创建的两条（测试库可能残留历史运行的数据）。
	mine := 0
	for _, h := range history {
		if h.ID == id1 || h.ID == id2 {
			mine++
		}
	}
	if mine != 2 {
		t.Fatalf("历史数量错误: %d", mine)
	}

	// 撤回。
	if err := s.WithdrawAnnouncement(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ = s.LatestAnnouncement(ctx)
	if got != nil {
		t.Fatalf("撤回后不应有有效公告: %+v", got)
	}

	// 更新策略：设置后可读回（单行表跨运行持久，不断言初始值）。
	if err := s.SetUpdatePolicy(ctx, true); err != nil {
		t.Fatal(err)
	}
	force, err := s.GetUpdatePolicy(ctx)
	if err != nil || !force {
		t.Fatalf("设置 true 后应读回 true: %v err=%v", force, err)
	}
	if err := s.SetUpdatePolicy(ctx, false); err != nil {
		t.Fatal(err)
	}
	force, err = s.GetUpdatePolicy(ctx)
	if err != nil || force {
		t.Fatalf("设置 false 后应读回 false: %v err=%v", force, err)
	}
}

func TestCleanupExpired(t *testing.T) {
	s := openTestStore(t)
	if err := s.CleanupExpired(context.Background()); err != nil {
		t.Fatalf("清理失败: %v", err)
	}
}

// jsonEqual 比较两段 JSON 的语义相等（忽略键序与空白）。
func jsonEqual(a, b []byte) bool {
	var va, vb any
	if json.Unmarshal(a, &va) != nil || json.Unmarshal(b, &vb) != nil {
		return false
	}
	return reflect.DeepEqual(va, vb)
}

func ptrTime(t time.Time) *time.Time { return &t }
func ptrString(s string) *string     { return &s }
