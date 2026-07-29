package e2e_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/action"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/schema"
)

func TestStrategyRunOnceCommitsResultAndFullTarget(t *testing.T) {
	repo, err := store.Open(filepath.Join(t.TempDir(), "strategy.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}

	source := `def run(context, data, params):
    return {"action":"rebalance","targets":[{"instrument_id":"BTC-USDT-SPOT","quantity":"0.5"}]}
`
	strategy := domain.Strategy{
		ID: "demo", Name: "demo",
		ManifestYAML: "api_version: moox.strategy/v1\nentrypoint: strategy.py:run\ninput:\n  history_bars: 1\n",
		SourceCode:   source, SourceHash: fmt.Sprintf("%x", sha256.Sum256([]byte(source))),
		CreatedAt: time.UnixMilli(1000),
	}
	if err := repo.SaveStrategy(context.Background(), strategy); err != nil {
		t.Fatal(err)
	}
	logicalAccountID := "logical-1"
	if err := repo.CreateRunner(context.Background(), domain.StrategyRunner{
		ID: "runner-1", StrategyID: strategy.ID, SpaceID: "space", ViewID: "view-1",
		Frequency: "1m", ParamsJSON: json.RawMessage(`{}`),
		LogicalAccountID: &logicalAccountID, Status: domain.RunnerStatusEnabled,
		CreatedAt: time.UnixMilli(1000), UpdatedAt: time.UnixMilli(1000),
	}); err != nil {
		t.Fatal(err)
	}

	workerPath := filepath.Join("..", "pyworker", "worker.py")
	runtime, err := engine.New(context.Background(), "python3", workerPath)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	service := &action.Service{Repo: repo, Engine: runtime}
	trigger := time.Now().UTC().Truncate(time.Second)
	output, inputHash, err := service.Evaluate(
		context.Background(),
		domain.ExecutionRequest{
			RequestID: "result-1", StrategyID: strategy.ID, RunnerID: "runner-1",
			TriggerBarTime: trigger.Format(time.RFC3339Nano), Namespace: "default",
			Params: map[string]any{},
			Data: []map[string]any{{
				"time": trigger.Format(time.RFC3339Nano),
			}},
		},
		strategy,
	)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := service.Commit(
		context.Background(),
		domain.StrategyResult{
			ID: "result-1", RunnerID: "runner-1", StrategyID: strategy.ID,
			TriggerBarTime: trigger, Namespace: "default", InputHash: inputHash,
			Action: output.Action, CreatedAt: trigger.Add(time.Second),
		},
		output,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Created || outcome.Result.CommandSequence == nil ||
		*outcome.Result.CommandSequence != 1 {
		t.Fatalf("outcome=%+v", outcome)
	}
	runner, err := repo.GetRunner(context.Background(), "runner-1")
	if err != nil {
		t.Fatal(err)
	}
	if string(runner.CurrentTargetsJSON) !=
		`[{"instrument_id":"BTC-USDT-SPOT","quantity":"0.5"}]` {
		t.Fatalf("targets=%s", runner.CurrentTargetsJSON)
	}
	stats, err := repo.PendingOutboxStats(context.Background())
	if err != nil || stats.PendingCount != 1 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
}
