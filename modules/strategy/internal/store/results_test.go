package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
)

func TestResultStoreRoundTrip(t *testing.T) {
	repo := openCurrentStore(t)
	seedStrategy(t, repo, "strategy-1")
	seedRunner(t, repo, "runner-1", "strategy-1", domain.RunnerStatusEnabled)
	want := domain.StrategyResult{
		ID: "result-1", RunnerID: "runner-1", StrategyID: "strategy-1",
		TriggerBarTime: time.UnixMilli(4000).UTC(), Namespace: "default",
		InputHash: "input-hash", Action: domain.ActionRebalance,
		CreatedAt: time.UnixMilli(5000).UTC(),
	}
	if _, err := repo.CommitResult(context.Background(), CommitResultRequest{
		Result: want,
		Output: domain.Output{
			Action:  domain.ActionRebalance,
			Targets: []domain.InstrumentTarget{},
		},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetResult(context.Background(), want.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.CommandSequence == nil || *got.CommandSequence != 1 {
		t.Fatalf("CommandSequence = %v", got.CommandSequence)
	}
	if got.Action != want.Action || !got.TriggerBarTime.Equal(want.TriggerBarTime) {
		t.Fatalf("GetResult() = %+v, want %+v", got, want)
	}
}

func seedRunner(t *testing.T, repo *Store, id, strategyID string, status domain.RunnerStatus) {
	t.Helper()
	if err := repo.CreateRunner(context.Background(), domain.StrategyRunner{
		ID: id, StrategyID: strategyID, SpaceID: "space", ViewID: "view", Frequency: "1m",
		ParamsJSON: json.RawMessage(`{}`), Status: status, CurrentTargetsJSON: json.RawMessage(`[]`),
		CreatedAt: time.UnixMilli(2000).UTC(), UpdatedAt: time.UnixMilli(2000).UTC(),
	}); err != nil {
		t.Fatal(err)
	}
}
