package e2e_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/rpc"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
	"github.com/mooyang-code/moox/modules/strategy/schema"
)

func TestStrategyRunnerResultAndTargetQueriesE2E(t *testing.T) {
	repo, err := store.Open(filepath.Join(t.TempDir(), "strategy.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	strategy := domain.Strategy{
		ID: "momentum", Name: "momentum", ManifestYAML: "api_version: moox.strategy/v1",
		SourceCode: "def run(context, data, params): return {'action':'hold'}",
		SourceHash: "hash-momentum", CreatedAt: time.UnixMilli(1000),
	}
	if err := repo.SaveStrategy(context.Background(), strategy); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRunner(context.Background(), domain.StrategyRunner{
		ID: "runner-1", StrategyID: strategy.ID, SpaceID: "space-1", ViewID: "view-1",
		Frequency: "1h", ParamsJSON: json.RawMessage(`{}`),
		Status: domain.RunnerStatusEnabled, CreatedAt: time.UnixMilli(2000),
		UpdatedAt: time.UnixMilli(2000),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CommitResult(context.Background(), store.CommitResultRequest{
		Result: domain.StrategyResult{
			ID: "result-1", RunnerID: "runner-1", StrategyID: strategy.ID,
			TriggerBarTime: time.UnixMilli(3000), Namespace: "default",
			InputHash: "hash-input", Action: domain.ActionRebalance,
			CreatedAt: time.UnixMilli(4000),
		},
		Output: domain.Output{
			Action: domain.ActionRebalance,
			Targets: []domain.InstrumentTarget{{
				InstrumentID: "BTC-USDT-SPOT", Quantity: "1",
			}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	service := &rpc.Service{Repo: repo}
	runners, err := service.ListRunners(context.Background(), &strategypb.ListRunnersReq{
		SpaceId: "space-1",
	})
	if err != nil || runners.GetRetInfo().GetCode() != 0 ||
		len(runners.GetRunners()) != 1 {
		t.Fatalf("runners=%+v err=%v", runners, err)
	}
	results, err := service.ListStrategyResults(
		context.Background(),
		&strategypb.ListStrategyResultsReq{RunnerId: "runner-1"},
	)
	if err != nil || results.GetRetInfo().GetCode() != 0 ||
		len(results.GetResults()) != 1 {
		t.Fatalf("results=%+v err=%v", results, err)
	}
	targets, err := service.ListStrategyTargets(
		context.Background(),
		&strategypb.ListStrategyTargetsReq{RunnerId: "runner-1"},
	)
	if err != nil || targets.GetRetInfo().GetCode() != 0 ||
		targets.GetCommandSequence() != 1 || len(targets.GetTargets()) != 1 ||
		targets.GetTargets()[0].GetQuantity() != "1" {
		t.Fatalf("targets=%+v err=%v", targets, err)
	}
}
