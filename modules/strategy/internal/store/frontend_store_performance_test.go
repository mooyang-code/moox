package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
)

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
