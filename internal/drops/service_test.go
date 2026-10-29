package drops

import (
	"strings"
	"testing"
	"time"

	"ap-cloud-server/internal/v3db"
)

func ptrInt(v int) *int { return &v }

func fixedService() *Service {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	return NewService(nil).WithClock(func() time.Time { return now })
}

func validCommission() Drop {
	return Drop{
		RecordID:    "record-12345678",
		Scene:       v3db.SceneCommission,
		Server:      "cn",
		Status:      ptrInt(1),
		CollectedAt: "2026-10-29T12:00:00+08:00",
		Comm:        "BIW Patrol Escort",
		Name:        "BIW巡视护卫",
		Items:       []Item{{Item: "Coins", Amount: 12}},
	}
}

func validResearch() Drop {
	return Drop{
		RecordID:    "record-87654321",
		Scene:       v3db.SceneResearch,
		Server:      "cn",
		Status:      ptrInt(0),
		CollectedAt: "2026-10-29T03:30:00Z",
		Series:      9,
		Project:     "Q-268-MI",
		Items:       []Item{{Item: "Coins", Tag: "t1", Amount: 3}},
	}
}

func TestValidateAcceptsSupportedScenes(t *testing.T) {
	s := fixedService()
	v, status, msg := s.validate(validCommission())
	if status != "" {
		t.Fatalf("委托应校验通过: %s %s", status, msg)
	}
	if v.status != 1 || !v.collectedAt.Equal(time.Date(2026, 10, 29, 4, 0, 0, 0, time.UTC)) {
		t.Fatalf("委托解析结果异常: %+v", v)
	}
	v, status, msg = s.validate(validResearch())
	if status != "" {
		t.Fatalf("科研应校验通过: %s %s", status, msg)
	}
	if v.drop.Series != 9 || v.drop.Project != "Q-268-MI" || v.status != 0 {
		t.Fatalf("科研解析结果异常: %+v", v)
	}
}

func TestValidateRejectsUnsupportedScene(t *testing.T) {
	s := fixedService()
	for _, scene := range []string{"battle", "", "COMMISSION"} {
		d := validCommission()
		d.Scene = scene
		if _, status, _ := s.validate(d); status != AckUnsupportedScene {
			t.Fatalf("scene=%q 应返回 unsupported_scene，实际 %q", scene, status)
		}
	}
}

func TestValidateRejectsBadFields(t *testing.T) {
	s := fixedService()
	cases := []struct {
		name string
		edit func(*Drop)
	}{
		{"record id 过短", func(d *Drop) { d.RecordID = "short" }},
		{"record id 含非法字符", func(d *Drop) { d.RecordID = "record/12345678" }},
		{"未知区服", func(d *Drop) { d.Server = "kr" }},
		{"缺少 status", func(d *Drop) { d.Status = nil }},
		{"status 越界", func(d *Drop) { d.Status = ptrInt(2) }},
		{"collected_at 无时区", func(d *Drop) { d.CollectedAt = "2026-10-29T12:00:00" }},
		{"时钟偏差过大", func(d *Drop) { d.CollectedAt = "2026-10-10T00:00:00Z" }},
		{"缺少物品", func(d *Drop) { d.Items = nil }},
		{"物品过多", func(d *Drop) { d.Items = make([]Item, 201) }},
		{"数量为零", func(d *Drop) { d.Items[0].Amount = 0 }},
		{"数量过大", func(d *Drop) { d.Items[0].Amount = 1_000_000_001 }},
		{"物品名非法", func(d *Drop) { d.Items[0].Item = " " }},
		{"标签过长", func(d *Drop) { d.Items[0].Tag = strings.Repeat("a", 33) }},
		{"委托缺少 comm", func(d *Drop) { d.Comm = " " }},
		{"科研 series 越界", func(d *Drop) { d.Scene = v3db.SceneResearch; d.Series = 0; d.Project = "P" }},
		{"科研缺少 project", func(d *Drop) { d.Scene = v3db.SceneResearch; d.Series = 1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := validCommission()
			tc.edit(&d)
			if _, status, _ := s.validate(d); status != AckInvalid {
				t.Fatalf("应返回 invalid，实际 %q", status)
			}
		})
	}
}

func TestValidateTrimsItems(t *testing.T) {
	s := fixedService()
	d := validCommission()
	d.Items = []Item{{Item: "  Coins  ", Tag: "  t1  ", Amount: 5}}
	v, status, msg := s.validate(d)
	if status != "" {
		t.Fatalf("校验失败: %s %s", status, msg)
	}
	if v.drop.Items[0].Item != "Coins" || v.drop.Items[0].Tag != "t1" {
		t.Fatalf("物品未规范化: %+v", v.drop.Items)
	}
}

func TestValidateClearsCrossSceneFields(t *testing.T) {
	s := fixedService()
	d := validCommission()
	d.Series = 7
	d.Project = "leak"
	v, _, _ := s.validate(d)
	if v.drop.Series != 0 || v.drop.Project != "" {
		t.Fatalf("委托不应保留科研字段: %+v", v.drop)
	}

	d = validResearch()
	d.Comm = "leak"
	d.Name = "leak"
	v, _, _ = s.validate(d)
	if v.drop.Comm != "" || v.drop.Name != "" {
		t.Fatalf("科研不应保留委托字段: %+v", v.drop)
	}
}

func TestIsPublicItem(t *testing.T) {
	for _, item := range []string{"PlaceHolder", "placeholder", "U-abc", "U?", "U?x"} {
		if IsPublicItem(item) {
			t.Fatalf("%q 不应公开展示", item)
		}
	}
	for _, item := range []string{"Coins", "Cube", "U"} {
		if !IsPublicItem(item) {
			t.Fatalf("%q 应公开展示", item)
		}
	}
}
