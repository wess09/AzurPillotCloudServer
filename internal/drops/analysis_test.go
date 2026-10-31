package drops

import (
	"context"
	"fmt"
	"math"
	"os"
	"sort"
	"testing"
	"time"

	"ap-cloud-server/internal/v3db"
	"gorm.io/gorm/clause"
)

func TestAnalysisWeightedRatesAndUnknownTime(t *testing.T) {
	var fast, slow, combined AnalysisMetrics
	fast.add(10, 2, 1, 3600, false)
	for i := 0; i < 9; i++ {
		slow.add(1, 0, 0, 36000, true)
	}
	combined.merge(fast)
	combined.merge(slow)
	combined.calculate()
	want := float64(19) / 91
	if combined.PerHour == nil || math.Abs(*combined.PerHour-want) > 1e-10 || combined.Samples != 10 || combined.SuccessSamples != 9 || combined.SuccessAmount != 9 {
		t.Fatalf("weighted result: %+v", combined)
	}
	combined.add(0, 0, 0, 0, false)
	combined.calculate()
	if combined.PerHour != nil || combined.Min != 0 || combined.Occurrences != 10 {
		t.Fatalf("zero drop / unknown time: %+v", combined)
	}
}

func TestAnalysisCommissionBatchesSuccessAndUnknownDuration(t *testing.T) {
	dsn := os.Getenv("TEMPLATE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEMPLATE_TEST_DATABASE_URL for PostgreSQL analysis integration tests")
	}
	ctx := context.Background()
	connection, err := v3db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	db := connection.DB
	stamp := time.Now().UTC().Truncate(time.Microsecond)
	prefix := "analysis-commission-" + time.Now().Format("150405.000000000")
	ids := make([]any, 0, 1052)
	events := make([]v3db.DropEvent, 0, 1052)
	for i := 0; i < 1052; i++ {
		id := fmt.Sprintf("%s-%04d", prefix, i)
		comm, name := "Small Merchant Escort", "小型商船护卫"
		if i >= 1001 {
			comm, name = prefix+"-unknown", "未收录委托"
		}
		ids = append(ids, id)
		events = append(events, v3db.DropEvent{RecordID: id, Scene: "commission", Server: "cn", Comm: comm, CommName: name, Status: int16(i % 2), CollectedAt: stamp, ReceivedAt: stamp})
	}
	t.Cleanup(func() {
		for _, model := range []any{&v3db.CommissionItem{}, &v3db.DropEvent{}} {
			db.Where(clause.IN{Column: "record_id", Values: ids}).Delete(model)
		}
	})
	if err := db.CreateInBatches(events, 100).Error; err != nil {
		t.Fatal(err)
	}
	items := []v3db.CommissionItem{{RecordID: ids[1].(string), Item: "Cubes", Amount: 3}, {RecordID: ids[1].(string), Item: "Cubes_2", Amount: 2}, {RecordID: ids[1001].(string), Item: "Cubes", Amount: 1}}
	if err := db.Create(&items).Error; err != nil {
		t.Fatal(err)
	}
	q := AnalysisQuery{Scene: "commission", Target: "category:cube", GroupBy: "project", From: stamp, To: stamp.Add(time.Microsecond), Server: "cn"}
	result, err := NewService(db).Analysis(ctx, q)
	if err != nil || result.Samples != 1052 || result.Occurrences != 2 || result.Amount != 6 || result.SuccessAmount != 6 || result.SuccessSamples != 526 || result.UnknownDurations != 51 || result.PerHour != nil {
		t.Fatalf("commission batches: %+v %v", result, err)
	}
	q.Category = "1"
	known, err := NewService(db).Analysis(ctx, q)
	if err != nil || known.Samples != 1001 || known.Occurrences != 1 || known.Max != 5 || known.Min != 0 || known.PerHour == nil || math.Abs(*known.PerHour-5.0/5005) > 1e-10 {
		t.Fatalf("known commission: %+v %v", known, err)
	}
}

func TestAnalysisCatalogTargetsAndDayNightIdentity(t *testing.T) {
	q := AnalysisQuery{Scene: "research", Target: "category:blueprint-rainbow", GroupBy: "project", From: time.Now(), To: time.Now().Add(time.Hour)}
	items, unit, err := analysisTarget(q)
	if err != nil || unit != "张" || !items["BlueprintAgir"] || items["Coins"] {
		t.Fatalf("category: %v %s %v", items, unit, err)
	}
	q.Target = "item:BlueprintAgir"
	items, _, err = analysisTarget(q)
	if err != nil || !items["BlueprintAgir_2"] || items["BlueprintHakuryuu"] {
		t.Fatalf("single item: %v %v", items, err)
	}
	q.Target = "category:coins-and-oil"
	if _, _, err = analysisTarget(q); err == nil {
		t.Fatal("mixed/unknown category accepted")
	}
	day := eventMetadata(v3db.DropEvent{Scene: "commission", Comm: "Small Merchant Escort", CommName: "小型商船护卫"}, nil)
	night := eventMetadata(v3db.DropEvent{Scene: "commission", Comm: "Small Merchant Escort N", CommName: "小型商船护卫"}, nil)
	if day.Duration != 18000 || night.Duration != 21600 || day.Category == night.Category {
		t.Fatalf("same-title variants: %+v %+v", day, night)
	}
	ambiguous := eventMetadata(v3db.DropEvent{Scene: "commission", Comm: "not-in-catalog", CommName: "小型商船护卫"}, nil)
	if ambiguous.Duration != 0 {
		t.Fatalf("ambiguous name guessed a duration: %+v", ambiguous)
	}
}

func TestAnalysisRawCategoryUnionMappingAndRegrouping(t *testing.T) {
	dsn := os.Getenv("TEMPLATE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEMPLATE_TEST_DATABASE_URL for PostgreSQL analysis integration tests")
	}
	ctx := context.Background()
	connection, err := v3db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	db := connection.DB
	prefix := "analysis-" + time.Now().Format("150405.000000000")
	stamp := time.Now().UTC().Truncate(time.Microsecond)
	keys := []string{}
	for key, project := range catalog.Research {
		if project.Series == 4 {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	a := catalog.Research[keys[0]]
	b := a
	for _, key := range keys {
		if catalog.Research[key].Duration != a.Duration {
			b = catalog.Research[key]
			break
		}
	}
	if a.Duration == b.Duration {
		t.Fatal("fixture requires two distinct task durations")
	}
	ids := []any{prefix + "a", prefix + "b", prefix + "c", prefix + "d"}
	unknown := "U-" + prefix
	t.Cleanup(func() {
		for _, model := range []any{&v3db.ResearchItem{}, &v3db.CommissionItem{}, &v3db.DropEvent{}} {
			db.Where(clause.IN{Column: "record_id", Values: ids}).Delete(model)
		}
		db.Where(clause.Eq{Column: "unknown_id", Value: unknown}).Delete(&v3db.TemplateItemMapping{})
	})
	events := []v3db.DropEvent{}
	for i, id := range ids {
		project := a
		if i >= 2 {
			project = b
		}
		events = append(events, v3db.DropEvent{RecordID: id.(string), Scene: "research", Server: "cn", Series: 4, Project: project.ID, CollectedAt: stamp, ReceivedAt: stamp})
	}
	if err := db.Create(&events).Error; err != nil {
		t.Fatal(err)
	}
	var second string
	for _, item := range catalog.Items {
		if item.Category == "blueprint-rainbow" && item.ID != "BlueprintAgir" {
			second = item.ID
			break
		}
	}
	items := []v3db.ResearchItem{
		{RecordID: ids[0].(string), Item: "BlueprintAgir", Amount: 3},
		{RecordID: ids[2].(string), Item: "BlueprintAgir", Amount: 2},
		{RecordID: ids[2].(string), Item: second, Tag: "bonus", Amount: 4},
		{RecordID: ids[3].(string), Item: unknown, Amount: 1},
	}
	if err := db.Create(&items).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&v3db.TemplateItemMapping{UnknownID: unknown, ItemID: "BlueprintAgir"}).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	q := AnalysisQuery{Scene: "research", Target: "category:blueprint-rainbow", GroupBy: "series", From: stamp, To: stamp.Add(time.Microsecond), Server: "cn", Series: 4}
	result, err := service.Analysis(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	wantRate := float64(10*3600) / float64(2*a.Duration+2*b.Duration)
	if len(result.Groups) != 1 || result.Samples != 4 || result.Occurrences != 3 || result.Amount != 10 || result.Min != 0 || result.Max != 6 || result.ExtraAmount != 4 || result.PerHour == nil || math.Abs(*result.PerHour-wantRate) > 1e-10 {
		t.Fatalf("category union: %+v", result)
	}
	q.Target = "item:BlueprintAgir"
	single, err := service.Analysis(ctx, q)
	if err != nil || single.Amount != 6 || single.Samples != 4 || single.Occurrences != 3 || single.Max != 3 {
		t.Fatalf("single target: %+v %v", single, err)
	}
	q.Target, q.GroupBy, q.Duration = "category:blueprint-rainbow", "project", a.Duration
	filtered, err := service.Analysis(ctx, q)
	if err != nil || filtered.Samples != 2 || filtered.Amount != 3 || filtered.Min != 0 || filtered.Average != 1.5 {
		t.Fatalf("duration filter: %+v %v", filtered, err)
	}
}
