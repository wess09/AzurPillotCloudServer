package drops

import (
	"context"
	"os"
	"testing"
	"time"

	"ap-cloud-server/internal/v3db"
	"gorm.io/gorm/clause"
)

func TestORMQueriesCombineHoursAndKeepPublicThresholds(t *testing.T) {
	dsn := os.Getenv("TEMPLATE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEMPLATE_TEST_DATABASE_URL for PostgreSQL query integration tests")
	}
	ctx := context.Background()
	connection, err := v3db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.Close() }()
	db := connection.DB
	key := "orm-query-" + time.Now().Format("150405.000000000")
	hour := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, model := range []any{&v3db.CommissionHourly{}, &v3db.CommissionItemHourly{}, &v3db.CommissionCatalog{}} {
		t.Cleanup(func() { db.Where(clause.Eq{Column: "comm", Value: key}).Delete(model) })
	}
	for _, model := range []any{&v3db.ResearchHourly{}, &v3db.ResearchItemHourly{}} {
		t.Cleanup(func() { db.Where(clause.Eq{Column: "project", Value: key}).Delete(model) })
	}
	fixtures := []any{
		&v3db.CommissionHourly{Hour: hour, Server: "cn", Comm: key, Status: 1, Samples: 25},
		&v3db.CommissionHourly{Hour: hour.Add(time.Hour), Server: "cn", Comm: key, Status: 1, Samples: 26},
		&v3db.CommissionHourly{Hour: hour, Server: "cn", Comm: key, Status: 0, Samples: 50},
		&v3db.CommissionItemHourly{Hour: hour, Server: "cn", Comm: key, Status: 1, Item: "Coins", Occurrences: 2, Amount: 6},
		&v3db.CommissionItemHourly{Hour: hour.Add(time.Hour), Server: "cn", Comm: key, Status: 1, Item: "Coins", Occurrences: 3, Amount: 9},
		&v3db.CommissionCatalog{Comm: key, Name: "test escort", DurationSeconds: 3600},
		&v3db.ResearchHourly{Hour: hour, Server: "cn", Series: 9, Project: key, Samples: 3},
		&v3db.ResearchHourly{Hour: hour.Add(time.Hour), Server: "cn", Series: 9, Project: key, Samples: 4},
		&v3db.ResearchItemHourly{Hour: hour, Server: "cn", Series: 9, Project: key, Item: "Blueprint", Tag: "T3", Occurrences: 2, Amount: 5},
		&v3db.ResearchItemHourly{Hour: hour.Add(time.Hour), Server: "cn", Series: 9, Project: key, Item: "Blueprint", Tag: "T3", Occurrences: 3, Amount: 6},
	}
	for _, fixture := range fixtures {
		if err := db.Create(fixture).Error; err != nil {
			t.Fatal(err)
		}
	}
	service := NewService(db)
	end := hour.Add(2 * time.Hour)
	commission, err := service.Commission(ctx, CommissionQuery{From: hour, To: end, Server: "cn", Comm: key})
	if err != nil || len(commission) != 1 {
		t.Fatalf("commission: %+v %v", commission, err)
	}
	group := commission[0]
	if group.Samples != 51 || len(group.Items) != 1 || group.Items[0].Amount != 15 || group.Items[0].Occurrences != 5 || group.Name != "test escort" {
		t.Fatalf("wrong aggregate: %+v", group)
	}
	options, err := service.CommissionOptions(ctx, hour, end, "cn")
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, option := range options {
		if option.Comm == key {
			found++
		}
	}
	if found != 2 {
		t.Fatalf("options lost the small-sample group: %+v", options)
	}
	research, err := service.Research(ctx, ResearchQuery{From: hour, To: end, Server: "cn", Series: 9, Project: key, Item: "Blueprint", Tag: "T3"})
	if err != nil || len(research) != 1 || research[0].Samples != 7 || len(research[0].Items) != 1 || research[0].Items[0].Amount != 11 {
		t.Fatalf("research: %+v %v", research, err)
	}
	researchOptions, err := service.ResearchOptions(ctx, hour, end, "cn")
	if err != nil {
		t.Fatal(err)
	}
	found = 0
	for _, option := range researchOptions {
		if option.Project == key && option.Samples == 7 {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("research options: %+v", researchOptions)
	}
}
