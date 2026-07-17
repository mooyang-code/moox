package store

import (
	"context"
	"fmt"
	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"gorm.io/gorm"
	"testing"
	"time"
)

func newFrontendStore(t *testing.T) *Store {
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
	r := newFrontendStore(t)
	ctx := context.Background()
	for i, space := range []string{"s1", "s1", "s2"} {
		id := "b" + string(rune('1'+i))
		if err := r.db.Create(&domain.StrategyDefinition{StrategyID: "demo", Version: id, API: "moox.strategy/v1", SourceHash: id, ManifestYAML: "id: demo", SourceCode: "def run(): pass", Status: "enabled"}).Error; err != nil {
			t.Fatal(err)
		}
		if err := r.db.Create(&domain.Binding{BindingID: id, StrategyID: "demo", StrategyVersion: id, SpaceID: space, Status: "enabled"}).Error; err != nil {
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
	r := newFrontendStore(t)
	_, err := r.ListPerformancePoints(context.Background(), PerformanceFilter{BindingID: "b1", Source: "paper+live"})
	if err == nil {
		t.Fatal("mixed source must be rejected")
	}
}

func TestHealthSummaryReturnsUnknownWhenMissing(t *testing.T) {
	r := newFrontendStore(t)
	health, err := r.GetHealth(context.Background(), "missing")
	if err != nil || health.Status != "unknown" {
		t.Fatalf("health=%+v err=%v", health, err)
	}
}

func TestListRunningStrategiesScansLargeRunHistoryWithoutLoadingIt(t *testing.T) {
	r := newFrontendStore(t)
	if err := r.db.Create(&domain.StrategyDefinition{StrategyID: "large", Version: "1", API: "moox.strategy/v1", SourceHash: "hash", ManifestYAML: "id: large", SourceCode: "source", Status: "enabled"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.db.Create(&domain.Binding{BindingID: "large-binding", StrategyID: "large", StrategyVersion: "1", SpaceID: "space-large", Status: "enabled"}).Error; err != nil {
		t.Fatal(err)
	}
	runs := make([]domain.StrategyRun, 10000)
	for i := range runs {
		trigger := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Minute)
		runs[i] = domain.StrategyRun{RunID: fmt.Sprintf("run-%05d", i), BindingID: "large-binding", StrategyVersion: "1", Namespace: "default", TriggerBarTime: trigger.Format(time.RFC3339Nano), DataRevision: fmt.Sprintf("rev-%d", i), InputHash: fmt.Sprintf("hash-%d", i), Status: "accepted", Action: "hold", OutputJSON: `{"action":"hold","targets":[],"next_state":{}}`}
	}
	if err := r.db.CreateInBatches(&runs, 500).Error; err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	items, total, err := r.ListRuns(context.Background(), RunFilter{BindingID: "large-binding"}, Page{Number: 1, Size: 20})
	if err != nil {
		t.Fatal(err)
	}
	if total != 10000 || len(items) != 20 {
		t.Fatalf("total=%d items=%d", total, len(items))
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("paged query took %s", elapsed)
	}
}
