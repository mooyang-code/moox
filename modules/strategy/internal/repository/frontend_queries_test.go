package repository

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"gorm.io/gorm"
)

func newFrontendRepo(t *testing.T) *Repository {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(schema.AllSQL()).Error; err != nil {
		t.Fatal(err)
	}
	return New(db)
}

func TestListRunningStrategiesPaginatesAndFiltersBySpace(t *testing.T) {
	r := newFrontendRepo(t)
	ctx := context.Background()
	for i, space := range []string{"s1", "s1", "s2"} {
		id := "b" + string(rune('1'+i))
		if err := r.DB.Create(&domain.StrategyDefinition{StrategyID: "demo", Version: id, API: "moox.strategy/v1", SourceHash: id, ManifestYAML: "id: demo", SourceCode: "def run(): pass", Status: "enabled"}).Error; err != nil {
			t.Fatal(err)
		}
		if err := r.DB.Create(&domain.Binding{BindingID: id, StrategyID: "demo", StrategyVersion: id, SpaceID: space, Status: "enabled"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	items, total, err := r.ListRunningStrategies(ctx, RunningFilter{SpaceID: "s1"}, Page{Number: 1, Size: 1})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(items) != 1 {
		t.Fatalf("total=%d items=%d", total, len(items))
	}
}

func TestPerformanceQueryRejectsMixedSources(t *testing.T) {
	r := newFrontendRepo(t)
	_, err := r.ListPerformancePoints(context.Background(), PerformanceFilter{BindingID: "b1", Source: "paper+live"})
	if err == nil {
		t.Fatal("mixed source must be rejected")
	}
}

func TestHealthSummaryReturnsUnknownWhenMissing(t *testing.T) {
	r := newFrontendRepo(t)
	health, err := r.GetHealth(context.Background(), "missing")
	if err != nil || health.Status != "unknown" {
		t.Fatalf("health=%+v err=%v", health, err)
	}
}
