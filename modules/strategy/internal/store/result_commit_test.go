package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"google.golang.org/protobuf/proto"
)

func TestCommitResultHoldPreservesCurrentTargetsAndSequence(t *testing.T) {
	repo := commitTestStore(t, nil)
	seedRunnerState(t, repo, `[{"instrument_id":"BTC-USDT-SPOT","quantity":"1"}]`, 7)

	outcome, err := repo.CommitResult(context.Background(), CommitResultRequest{
		Result: commitTestResult("result-hold", time.UnixMilli(10_000), "hash-hold", domain.ActionHold),
		Output: domain.Output{Action: domain.ActionHold},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Created || outcome.Result.CommandSequence != nil {
		t.Fatalf("outcome = %+v", outcome)
	}
	runner := mustRunner(t, repo)
	if string(runner.CurrentTargetsJSON) != `[{"instrument_id":"BTC-USDT-SPOT","quantity":"1"}]` ||
		runner.CommandSequence != 7 {
		t.Fatalf("runner = %+v", runner)
	}
}

func TestCommitResultObserveOnlyRebalanceUpdatesTargetWithoutOutbox(t *testing.T) {
	repo := commitTestStore(t, nil)
	outcome, err := repo.CommitResult(context.Background(), CommitResultRequest{
		Result: commitTestResult("result-observe", time.UnixMilli(10_000), "hash-observe", domain.ActionRebalance),
		Output: domain.Output{Action: domain.ActionRebalance, Targets: []domain.InstrumentTarget{{
			InstrumentID: "BTC-USDT-SPOT", Quantity: "2",
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Result.CommandSequence == nil || *outcome.Result.CommandSequence != 1 {
		t.Fatalf("sequence = %v", outcome.Result.CommandSequence)
	}
	runner := mustRunner(t, repo)
	if runner.CommandSequence != 1 ||
		string(runner.CurrentTargetsJSON) != `[{"instrument_id":"BTC-USDT-SPOT","quantity":"2"}]` {
		t.Fatalf("runner = %+v", runner)
	}
	if countRows(t, repo, "t_strategy_outbox") != 0 {
		t.Fatal("observe-only rebalance wrote outbox")
	}
}

func TestCommitResultExecutingRebalanceAtomicallyAdvancesRunnerAndWritesOutbox(t *testing.T) {
	logicalAccountID := "logical-1"
	repo := commitTestStore(t, &logicalAccountID)
	outcome, err := repo.CommitResult(context.Background(), CommitResultRequest{
		Result: commitTestResult("result-live", time.UnixMilli(10_000), "hash-live", domain.ActionRebalance),
		Output: domain.Output{Action: domain.ActionRebalance, Targets: []domain.InstrumentTarget{{
			InstrumentID: "BTC-USDT-SPOT", Quantity: "2",
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Created || outcome.Result.CommandSequence == nil ||
		*outcome.Result.CommandSequence != 1 {
		t.Fatalf("outcome = %+v", outcome)
	}
	runner := mustRunner(t, repo)
	if runner.CommandSequence != 1 || runner.LastResultID == nil ||
		*runner.LastResultID != "result-live" || runner.LastSuccessAt == nil ||
		runner.LastError != nil {
		t.Fatalf("runner = %+v", runner)
	}
	message := decodeOnlyOutbox(t, repo)
	if message.GetEventId() != "result-live" || message.GetSubjectId() != logicalAccountID {
		t.Fatalf("message = %+v", message)
	}
	var payload tradeeventpb.LogicalAccountTargetRequested
	if err := proto.Unmarshal(message.GetPayload(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.GetTargetId() != "result-live" || payload.GetRunnerId() != "runner-1" ||
		payload.GetLogicalAccountId() != logicalAccountID || payload.GetCommandSequence() != 1 ||
		len(payload.GetTargets()) != 1 || payload.GetTargets()[0].GetQuantity() != "2" {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestCommitResultEmptyRebalanceWritesEmptyFullTarget(t *testing.T) {
	logicalAccountID := "logical-1"
	repo := commitTestStore(t, &logicalAccountID)
	_, err := repo.CommitResult(context.Background(), CommitResultRequest{
		Result: commitTestResult("result-empty", time.UnixMilli(10_000), "hash-empty", domain.ActionRebalance),
		Output: domain.Output{Action: domain.ActionRebalance, Targets: []domain.InstrumentTarget{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	message := decodeOnlyOutbox(t, repo)
	var payload tradeeventpb.LogicalAccountTargetRequested
	if err := proto.Unmarshal(message.GetPayload(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Targets) != 0 {
		t.Fatalf("empty FULL target = %#v", payload.Targets)
	}
}

func TestCommitResultLogicalRetrySameHashReturnsExistingResult(t *testing.T) {
	repo := commitTestStore(t, nil)
	trigger := time.UnixMilli(10_000)
	first, err := repo.CommitResult(context.Background(), CommitResultRequest{
		Result: commitTestResult("result-first", trigger, "same-hash", domain.ActionHold),
		Output: domain.Output{Action: domain.ActionHold},
	})
	if err != nil {
		t.Fatal(err)
	}
	retry, err := repo.CommitResult(context.Background(), CommitResultRequest{
		Result: commitTestResult("result-retry", trigger, "same-hash", domain.ActionHold),
		Output: domain.Output{Action: domain.ActionHold},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created || retry.Created || retry.Result.ID != first.Result.ID ||
		countRows(t, repo, "t_strategy_results") != 1 {
		t.Fatalf("first=%+v retry=%+v", first, retry)
	}
}

func TestCommitResultLogicalRetryDifferentHashConflicts(t *testing.T) {
	repo := commitTestStore(t, nil)
	trigger := time.UnixMilli(10_000)
	_, err := repo.CommitResult(context.Background(), CommitResultRequest{
		Result: commitTestResult("result-first", trigger, "hash-1", domain.ActionHold),
		Output: domain.Output{Action: domain.ActionHold},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.CommitResult(context.Background(), CommitResultRequest{
		Result: commitTestResult("result-conflict", trigger, "hash-2", domain.ActionHold),
		Output: domain.Output{Action: domain.ActionHold},
	})
	if !errors.Is(err, ErrLogicalResultConflict) {
		t.Fatalf("CommitResult() error = %v", err)
	}
}

func TestCommitResultConcurrentSequenceIsMonotonic(t *testing.T) {
	logicalAccountID := "logical-1"
	repo := commitTestStore(t, &logicalAccountID)
	const count = 8
	sequences := make(chan int64, count)
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for index := 0; index < count; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			outcome, err := repo.CommitResult(context.Background(), CommitResultRequest{
				Result: commitTestResult(
					fmt.Sprintf("result-%d", index),
					time.UnixMilli(int64(10_000+index)),
					fmt.Sprintf("hash-%d", index),
					domain.ActionRebalance,
				),
				Output: domain.Output{Action: domain.ActionRebalance, Targets: []domain.InstrumentTarget{}},
			})
			if err != nil {
				errs <- err
				return
			}
			sequences <- *outcome.Result.CommandSequence
		}(index)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	close(sequences)
	got := make([]int, 0, count)
	for sequence := range sequences {
		got = append(got, int(sequence))
	}
	sort.Ints(got)
	for index, sequence := range got {
		if sequence != index+1 {
			t.Fatalf("sequences = %v", got)
		}
	}
	if mustRunner(t, repo).CommandSequence != count {
		t.Fatalf("final runner sequence = %d", mustRunner(t, repo).CommandSequence)
	}
}

func TestCommitResultTransactionFailureRollsBackResultTargetSequenceAndOutbox(t *testing.T) {
	logicalAccountID := "logical-1"
	repo := commitTestStore(t, &logicalAccountID)
	if err := repo.db.Exec(`
		CREATE TRIGGER fail_strategy_outbox
		BEFORE INSERT ON t_strategy_outbox
		BEGIN
			SELECT RAISE(FAIL, 'injected outbox failure');
		END
	`).Error; err != nil {
		t.Fatal(err)
	}
	_, err := repo.CommitResult(context.Background(), CommitResultRequest{
		Result: commitTestResult("result-fail", time.UnixMilli(10_000), "hash-fail", domain.ActionRebalance),
		Output: domain.Output{Action: domain.ActionRebalance, Targets: []domain.InstrumentTarget{{
			InstrumentID: "BTC-USDT-SPOT", Quantity: "2",
		}}},
	})
	if err == nil {
		t.Fatal("CommitResult() succeeded despite outbox failure")
	}
	if countRows(t, repo, "t_strategy_results") != 0 ||
		countRows(t, repo, "t_strategy_outbox") != 0 {
		t.Fatal("transaction failure retained rows")
	}
	runner := mustRunner(t, repo)
	if runner.CommandSequence != 0 || string(runner.CurrentTargetsJSON) != "[]" ||
		runner.LastResultID != nil {
		t.Fatalf("runner changed after rollback: %+v", runner)
	}
}

func TestFailedAttemptUpdatesRunnerErrorWithoutCreatingResult(t *testing.T) {
	repo := commitTestStore(t, nil)
	if err := repo.RecordRunnerFailure(
		context.Background(),
		"runner-1",
		errors.New("python failed"),
		time.UnixMilli(12_000),
	); err != nil {
		t.Fatal(err)
	}
	runner := mustRunner(t, repo)
	if runner.LastError == nil || *runner.LastError != "python failed" ||
		runner.LastResultID != nil || countRows(t, repo, "t_strategy_results") != 0 {
		t.Fatalf("runner = %+v", runner)
	}
}

func commitTestStore(t *testing.T, logicalAccountID *string) *Store {
	t.Helper()
	repo := openCurrentStore(t)
	seedStrategy(t, repo, "strategy-1")
	if err := repo.CreateRunner(context.Background(), domain.StrategyRunner{
		ID: "runner-1", StrategyID: "strategy-1", SpaceID: "crypto", ViewID: "view-1",
		Frequency: "1m", ParamsJSON: json.RawMessage(`{}`), LogicalAccountID: logicalAccountID,
		Status: domain.RunnerStatusDisabled, CreatedAt: time.UnixMilli(1000),
		UpdatedAt: time.UnixMilli(1000),
	}); err != nil {
		t.Fatal(err)
	}
	return repo
}

func commitTestResult(
	id string,
	trigger time.Time,
	inputHash string,
	action domain.Action,
) domain.StrategyResult {
	return domain.StrategyResult{
		ID: id, RunnerID: "runner-1", StrategyID: "strategy-1",
		TriggerBarTime: trigger.UTC(), Namespace: "default", InputHash: inputHash,
		Action: action, CreatedAt: trigger.Add(time.Second).UTC(),
	}
}

func seedRunnerState(t *testing.T, repo *Store, targets string, sequence int64) {
	t.Helper()
	if err := repo.db.Exec(`
		UPDATE t_strategy_runners
		SET current_targets_json = ?, command_sequence = ?
		WHERE runner_id = 'runner-1'
	`, targets, sequence).Error; err != nil {
		t.Fatal(err)
	}
}

func mustRunner(t *testing.T, repo *Store) domain.StrategyRunner {
	t.Helper()
	runner, err := repo.GetRunner(context.Background(), "runner-1")
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func countRows(t *testing.T, repo *Store, table string) int64 {
	t.Helper()
	var count int64
	if err := repo.db.Table(table).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	return count
}

func decodeOnlyOutbox(t *testing.T, repo *Store) *eventpb.EventMessage {
	t.Helper()
	rows, err := repo.ListPendingOutbox(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("outbox rows = %d", len(rows))
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	message, err := registry.UnmarshalMessage(rows[0].EventData)
	if err != nil {
		t.Fatal(err)
	}
	return message
}
