package store

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"google.golang.org/protobuf/proto"
)

func TestResultStoreRoundTrip(t *testing.T) {
	repo := openCurrentStore(t)
	seedStrategy(t, repo, "strategy-1")
	logicalAccountID := "logical-1"
	if err := repo.CreateRunner(context.Background(), domain.StrategyRunner{ID: "runner-1", StrategyID: "strategy-1", SpaceID: "space", SourceViewID: "view", Frequency: "1m", LogicalAccountID: &logicalAccountID, Status: domain.RunnerStatusEnabled, CreatedAt: time.UnixMilli(1000), UpdatedAt: time.UnixMilli(1000)}); err != nil {
		t.Fatal(err)
	}
	want := domain.StrategyResult{
		ID: "result-1", RunnerID: "runner-1", StrategyID: "strategy-1",
		PeriodTime: time.UnixMilli(4000).UTC(),
		InputHash:  "input-hash", Action: domain.ActionRebalance,
		CreatedAt: time.UnixMilli(5000).UTC(),
	}
	if _, err := repo.CommitEvaluation(context.Background(), CommitEvaluationRequest{
		Result:     want,
		Evaluation: domain.Evaluation{Action: domain.ActionRebalance, Targets: []domain.InstrumentTarget{}},
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
	if got.Action != want.Action || !got.PeriodTime.Equal(want.PeriodTime) {
		t.Fatalf("GetResult() = %+v, want %+v", got, want)
	}
}

func TestCommitEvaluationAdvancesSequenceAndPublishesWeightEvent(t *testing.T) {
	repo := openCurrentStore(t)
	seedStrategy(t, repo, "strategy-1")
	logical := "logical-1"
	if err := repo.CreateRunner(context.Background(), domain.StrategyRunner{ID: "runner-1", StrategyID: "strategy-1", SpaceID: "space", SourceViewID: "view", Frequency: "1m", LogicalAccountID: &logical, Status: domain.RunnerStatusEnabled, CreatedAt: time.UnixMilli(1000), UpdatedAt: time.UnixMilli(1000)}); err != nil {
		t.Fatal(err)
	}
	result := domain.StrategyResult{ID: "result-1", RunnerID: "runner-1", StrategyID: "strategy-1", PeriodTime: time.UnixMilli(2000), InputHash: "hash-1", Action: domain.ActionRebalance, CreatedAt: time.UnixMilli(3000)}
	outcome, err := repo.CommitEvaluation(context.Background(), CommitEvaluationRequest{Result: result, Evaluation: domain.Evaluation{Action: domain.ActionRebalance, DebugInfo: map[string]any{"owner_generation": int64(7)}, Targets: []domain.InstrumentTarget{{InstrumentID: "BTC-USDT-SPOT", TargetWeight: "0.5"}}}})
	if err != nil || !outcome.Created || outcome.Result.CommandSequence == nil || *outcome.Result.CommandSequence != 1 {
		t.Fatalf("commit outcome=%+v err=%v", outcome, err)
	}
	runner, err := repo.GetRunner(context.Background(), "runner-1")
	if err != nil || runner.CommandSequence != 1 || runner.LastResultID == nil || *runner.LastResultID != "result-1" {
		t.Fatalf("runner=%+v err=%v", runner, err)
	}
	var outboxRow struct {
		EventData []byte `gorm:"column:event_data"`
	}
	if err := repo.db.Table("t_strategy_outbox").Select("event_data").Where("message_id = ?", "result-1").Scan(&outboxRow).Error; err != nil {
		t.Fatal(err)
	}
	eventData := outboxRow.EventData
	message, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := message.UnmarshalMessage(eventData)
	if err != nil {
		t.Fatal(err)
	}
	payload := &tradeeventpb.LogicalAccountTargetWeightRequested{}
	if err := proto.Unmarshal(decoded.GetPayload(), payload); err != nil {
		t.Fatal(err)
	}
	if got := payload.GetTargets()[0].GetTargetWeight(); got != "0.5" {
		t.Fatalf("target_weight=%q", got)
	}
	if got := payload.GetOwnerGeneration(); got != 7 {
		t.Fatalf("owner_generation=%d", got)
	}
}

func TestCommitEvaluationSameHashIsIdempotentAndHoldKeepsTargets(t *testing.T) {
	repo := openCurrentStore(t)
	seedStrategy(t, repo, "strategy-1")
	if err := repo.CreateRunner(context.Background(), domain.StrategyRunner{ID: "runner-1", StrategyID: "strategy-1", SpaceID: "space", SourceViewID: "view", Frequency: "1m", Status: domain.RunnerStatusEnabled, CreatedAt: time.UnixMilli(1000), UpdatedAt: time.UnixMilli(1000)}); err != nil {
		t.Fatal(err)
	}
	first := domain.StrategyResult{ID: "result-1", RunnerID: "runner-1", StrategyID: "strategy-1", PeriodTime: time.UnixMilli(2000), InputHash: "hash-1", Action: domain.ActionRebalance, CreatedAt: time.UnixMilli(3000)}
	eval := domain.Evaluation{Action: domain.ActionRebalance, Targets: []domain.InstrumentTarget{{InstrumentID: "BTC", TargetWeight: "1"}}}
	if _, err := repo.CommitEvaluation(context.Background(), CommitEvaluationRequest{Result: first, Evaluation: eval}); err != nil {
		t.Fatal(err)
	}
	retry := first
	retry.ID = "different-id"
	retry.CreatedAt = time.UnixMilli(4000)
	outcome, err := repo.CommitEvaluation(context.Background(), CommitEvaluationRequest{Result: retry, Evaluation: eval})
	if err != nil || outcome.Created || outcome.Result.ID != first.ID {
		t.Fatalf("retry=%+v err=%v", outcome, err)
	}
	hold := domain.StrategyResult{ID: "result-2", RunnerID: "runner-1", StrategyID: "strategy-1", PeriodTime: time.UnixMilli(3000), InputHash: "hash-2", Action: domain.ActionHold, CreatedAt: time.UnixMilli(5000)}
	if _, err := repo.CommitEvaluation(context.Background(), CommitEvaluationRequest{Result: hold, Evaluation: domain.Evaluation{Action: domain.ActionHold, Targets: eval.Targets}}); err != nil {
		t.Fatal(err)
	}
	runner, err := repo.GetRunner(context.Background(), "runner-1")
	if err != nil {
		t.Fatal(err)
	}
	if runner.CommandSequence != 1 || string(runner.CurrentTargetsJSON) != `[{"instrument_id":"BTC","target_weight":"1"}]` {
		t.Fatalf("runner=%+v", runner)
	}
	var outboxCount int64
	if err := repo.db.Table("t_strategy_outbox").Count(&outboxCount).Error; err != nil {
		t.Fatal(err)
	}
	if outboxCount != 0 {
		t.Fatal("observe-only runner should not publish an outbox event")
	}
}

func TestCommitEvaluationOwnerGenerationChangeForcesRebalance(t *testing.T) {
	repo := openCurrentStore(t)
	seedStrategy(t, repo, "strategy-1")
	logical := "logical-1"
	if err := repo.CreateRunner(context.Background(), domain.StrategyRunner{ID: "runner-1", StrategyID: "strategy-1", SpaceID: "space", SourceViewID: "view", Frequency: "1m", LogicalAccountID: &logical, Status: domain.RunnerStatusEnabled, CreatedAt: time.UnixMilli(1000), UpdatedAt: time.UnixMilli(1000)}); err != nil {
		t.Fatal(err)
	}
	targets := []domain.InstrumentTarget{{InstrumentID: "BTC", TargetWeight: "1"}}
	first := domain.StrategyResult{ID: "result-1", RunnerID: "runner-1", StrategyID: "strategy-1", PeriodTime: time.UnixMilli(2000), InputHash: "hash-1", Action: domain.ActionRebalance, CreatedAt: time.UnixMilli(3000)}
	if _, err := repo.CommitEvaluation(context.Background(), CommitEvaluationRequest{Result: first, OwnerGeneration: 1, Evaluation: domain.Evaluation{Action: domain.ActionRebalance, DebugInfo: map[string]any{"owner_generation": int64(1)}, Targets: targets}}); err != nil {
		t.Fatal(err)
	}
	second := domain.StrategyResult{ID: "result-2", RunnerID: "runner-1", StrategyID: "strategy-1", PeriodTime: time.UnixMilli(4000), InputHash: "hash-2", Action: domain.ActionHold, CreatedAt: time.UnixMilli(5000)}
	outcome, err := repo.CommitEvaluation(context.Background(), CommitEvaluationRequest{Result: second, OwnerGeneration: 2, Evaluation: domain.Evaluation{Action: domain.ActionHold, Targets: targets}})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Result.Action != domain.ActionRebalance || !outcome.Created || outcome.Result.CommandSequence == nil || *outcome.Result.CommandSequence != 2 {
		t.Fatalf("outcome=%+v", outcome)
	}
}

func TestCommitEvaluationReplaysSamePeriodAfterRunnerRebind(t *testing.T) {
	repo := openCurrentStore(t)
	seedStrategy(t, repo, "strategy-1")
	seedStrategy(t, repo, "strategy-2")
	logicalAccountID := "logical-1"
	if err := repo.CreateRunner(context.Background(), domain.StrategyRunner{ID: "runner-1", StrategyID: "strategy-1", SpaceID: "space", SourceViewID: "view", Frequency: "1m", LogicalAccountID: &logicalAccountID, Status: domain.RunnerStatusEnabled, CreatedAt: time.UnixMilli(1000), UpdatedAt: time.UnixMilli(1000)}); err != nil {
		t.Fatal(err)
	}
	period := time.UnixMilli(2000).UTC()
	eval := domain.Evaluation{Action: domain.ActionRebalance, Targets: []domain.InstrumentTarget{{InstrumentID: "BTC", TargetWeight: "1"}}}
	first := domain.StrategyResult{ID: "result-1", RunnerID: "runner-1", StrategyID: "strategy-1", PeriodTime: period, InputHash: "hash-1", Action: domain.ActionRebalance, CreatedAt: time.UnixMilli(3000).UTC()}
	if _, err := repo.CommitEvaluation(context.Background(), CommitEvaluationRequest{Result: first, Evaluation: eval}); err != nil {
		t.Fatal(err)
	}
	runner, err := repo.GetRunner(context.Background(), "runner-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetRunnerStatus(context.Background(), "runner-1", domain.RunnerStatusDisabled, time.UnixMilli(3500).UTC()); err != nil {
		t.Fatal(err)
	}
	runner.StrategyID = "strategy-2"
	runner.UpdatedAt = time.UnixMilli(4000).UTC()
	if err := repo.UpdateRunner(context.Background(), runner); err != nil {
		t.Fatal(err)
	}
	runner.StrategyID = "strategy-1"
	runner.UpdatedAt = time.UnixMilli(5000).UTC()
	if err := repo.UpdateRunner(context.Background(), runner); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetRunnerStatus(context.Background(), "runner-1", domain.RunnerStatusEnabled, time.UnixMilli(6000).UTC()); err != nil {
		t.Fatal(err)
	}
	replay := first
	replay.CreatedAt = time.UnixMilli(7000).UTC()
	outcome, err := repo.CommitEvaluation(context.Background(), CommitEvaluationRequest{Result: replay, Evaluation: eval})
	if err != nil || !outcome.Created {
		t.Fatalf("replay outcome=%+v err=%v", outcome, err)
	}
	if outcome.Result.CommandSequence == nil || *outcome.Result.CommandSequence != 2 {
		t.Fatalf("replay sequence=%v", outcome.Result.CommandSequence)
	}
	var outboxCount int64
	if err := repo.db.Table("t_strategy_outbox").Count(&outboxCount).Error; err != nil {
		t.Fatal(err)
	}
	if outboxCount != 1 {
		t.Fatalf("outbox count=%d, want 1 after stale outbox cleanup", outboxCount)
	}
	if _, err := repo.GetResult(context.Background(), first.ID); err != nil {
		t.Fatalf("historical result was deleted: %v", err)
	}
	results, err := repo.ListResults(context.Background(), ResultFilter{RunnerID: "runner-1"})
	if err != nil || len(results) != 2 {
		t.Fatalf("historical result count=%d err=%v", len(results), err)
	}
}

func seedRunner(t *testing.T, repo *Store, id, strategyID string, status domain.RunnerStatus) {
	t.Helper()
	if err := repo.CreateRunner(context.Background(), domain.StrategyRunner{
		ID: id, StrategyID: strategyID, SpaceID: "space", SourceViewID: "view", Frequency: "1m",
		Status: status, CurrentTargetsJSON: []byte(`[]`),
		CreatedAt: time.UnixMilli(2000).UTC(), UpdatedAt: time.UnixMilli(2000).UTC(),
	}); err != nil {
		t.Fatal(err)
	}
}
