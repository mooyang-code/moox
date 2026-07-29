package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
)

func TestRunnerStoreRoundTripPreservesNullableFields(t *testing.T) {
	repo := openCurrentStore(t)
	seedStrategy(t, repo, "strategy-1")
	logicalAccountID := "logical-account-1"
	want := domain.StrategyRunner{
		ID:                 "runner-1",
		StrategyID:         "strategy-1",
		SpaceID:            "space-1",
		ViewID:             "view-1",
		Frequency:          "1m",
		ParamsJSON:         json.RawMessage(`{"fast":12}`),
		LogicalAccountID:   &logicalAccountID,
		Status:             domain.RunnerStatusDisabled,
		CurrentTargetsJSON: json.RawMessage(`[]`),
		CommandSequence:    0,
		CreatedAt:          time.UnixMilli(2000).UTC(),
		UpdatedAt:          time.UnixMilli(3000).UTC(),
	}
	if err := repo.CreateRunner(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetRunner(context.Background(), want.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LogicalAccountID == nil || *got.LogicalAccountID != logicalAccountID {
		t.Fatalf("LogicalAccountID = %v", got.LogicalAccountID)
	}
	if got.LastResultID != nil || got.LastSuccessAt != nil || got.LastError != nil {
		t.Fatalf("nullable runner health fields = %+v", got)
	}
}

func TestCreateRunnerInitializesTargetSequenceAndHealth(t *testing.T) {
	repo := openCurrentStore(t)
	seedStrategy(t, repo, "strategy-1")
	now := time.UnixMilli(4000).UTC()
	runner := domain.StrategyRunner{
		ID: "runner-1", StrategyID: "strategy-1", SpaceID: "space-1", ViewID: "view-1",
		Frequency: "1m", ParamsJSON: json.RawMessage(`{}`), Status: domain.RunnerStatusDisabled,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := repo.CreateRunner(context.Background(), runner); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetRunner(context.Background(), runner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.CurrentTargetsJSON) != "[]" || got.CommandSequence != 0 ||
		got.LastResultID != nil || got.LastSuccessAt != nil || got.LastError != nil {
		t.Fatalf("runner initial state = %+v", got)
	}
}

func TestUpdateRunnerRequiresDisabledStatus(t *testing.T) {
	repo := openCurrentStore(t)
	seedStrategy(t, repo, "strategy-1")
	runner := newRunner("runner-1", "strategy-1", domain.RunnerStatusEnabled)
	if err := repo.CreateRunner(context.Background(), runner); err != nil {
		t.Fatal(err)
	}
	runner.Frequency = "5m"
	if err := repo.UpdateRunner(context.Background(), runner); !errors.Is(err, ErrRunnerEnabled) {
		t.Fatalf("UpdateRunner() error = %v", err)
	}
}

func TestSwitchRunnerStrategyPreservesTargetAndSequence(t *testing.T) {
	repo := openCurrentStore(t)
	seedStrategy(t, repo, "strategy-1")
	seedStrategy(t, repo, "strategy-2")
	runner := newRunner("runner-1", "strategy-1", domain.RunnerStatusDisabled)
	runner.CurrentTargetsJSON = json.RawMessage(`[{"instrument_id":"BTC-USDT-SPOT","quantity":"1"}]`)
	runner.CommandSequence = 7
	resultID, lastError := "result-1", "old error"
	success := time.UnixMilli(8000).UTC()
	runner.LastResultID, runner.LastSuccessAt, runner.LastError = &resultID, &success, &lastError
	if err := repo.CreateRunner(context.Background(), runner); err != nil {
		t.Fatal(err)
	}
	if err := repo.db.Exec(`
		UPDATE t_strategy_runners
		SET current_targets_json = ?, command_sequence = ?, last_result_id = ?,
		    last_success_at = ?, last_error = ?
		WHERE runner_id = ?
	`, string(runner.CurrentTargetsJSON), runner.CommandSequence, resultID,
		success.UnixMilli(), lastError, runner.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.SwitchRunnerStrategy(context.Background(), runner.ID, "strategy-2", time.UnixMilli(9000)); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetRunner(context.Background(), runner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.StrategyID != "strategy-2" || string(got.CurrentTargetsJSON) != string(runner.CurrentTargetsJSON) || got.CommandSequence != 7 {
		t.Fatalf("switched runner = %+v", got)
	}
	if got.LastResultID != nil || got.LastSuccessAt != nil || got.LastError != nil {
		t.Fatalf("artifact-specific health was retained: %+v", got)
	}
}

func TestSwitchRunnerStrategyRequiresDisabledStatus(t *testing.T) {
	repo := openCurrentStore(t)
	seedStrategy(t, repo, "strategy-1")
	seedStrategy(t, repo, "strategy-2")
	runner := newRunner("runner-1", "strategy-1", domain.RunnerStatusEnabled)
	if err := repo.CreateRunner(context.Background(), runner); err != nil {
		t.Fatal(err)
	}
	if err := repo.SwitchRunnerStrategy(context.Background(), runner.ID, "strategy-2", time.Now()); !errors.Is(err, ErrRunnerEnabled) {
		t.Fatalf("SwitchRunnerStrategy() error = %v", err)
	}
}

func TestEnableRunnerRejectsLogicalAccountOwnedByAnotherRunner(t *testing.T) {
	repo := openCurrentStore(t)
	seedStrategy(t, repo, "strategy-1")
	logicalID := "logical-1"
	first := newRunner("runner-1", "strategy-1", domain.RunnerStatusDisabled)
	first.LogicalAccountID = &logicalID
	second := newRunner("runner-2", "strategy-1", domain.RunnerStatusDisabled)
	second.LogicalAccountID = &logicalID
	if err := repo.CreateRunner(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRunner(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetRunnerStatus(context.Background(), first.ID, domain.RunnerStatusEnabled, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetRunnerStatus(context.Background(), second.ID, domain.RunnerStatusEnabled, time.Now()); err == nil {
		t.Fatal("second enabled owner was accepted")
	}
}

func TestObserveOnlyRunnerHasNoLogicalAccount(t *testing.T) {
	repo := openCurrentStore(t)
	seedStrategy(t, repo, "strategy-1")
	runner := newRunner("runner-1", "strategy-1", domain.RunnerStatusDisabled)
	if err := repo.CreateRunner(context.Background(), runner); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetRunner(context.Background(), runner.ID)
	if err != nil || got.LogicalAccountID != nil {
		t.Fatalf("runner=%+v err=%v", got, err)
	}
}

func newRunner(id, strategyID string, status domain.RunnerStatus) domain.StrategyRunner {
	now := time.UnixMilli(5000).UTC()
	return domain.StrategyRunner{
		ID: id, StrategyID: strategyID, SpaceID: "space-1", ViewID: "view-1", Frequency: "1m",
		ParamsJSON: json.RawMessage(`{}`), Status: status, CurrentTargetsJSON: json.RawMessage(`[]`),
		CreatedAt: now, UpdatedAt: now,
	}
}

func seedStrategy(t *testing.T, repo *Store, id string) {
	t.Helper()
	if err := repo.SaveStrategy(context.Background(), domain.Strategy{
		ID: id, Name: id, ManifestYAML: "{}", SourceCode: "pass", SourceHash: id,
		CreatedAt: time.UnixMilli(1000).UTC(),
	}); err != nil {
		t.Fatal(err)
	}
}
