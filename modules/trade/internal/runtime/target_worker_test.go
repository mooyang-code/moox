package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type logicalTargetStoreStub struct {
	mu       sync.Mutex
	records  []store.LogicalAccountTargetRecord
	statuses []string
	err      error
}

func (s *logicalTargetStoreStub) GetLogicalAccountTarget(_ context.Context, space, logical string) (store.LogicalAccountTargetRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range s.records {
		if record.SpaceID == space && record.LogicalAccountID == logical {
			return record, s.err
		}
	}
	return store.LogicalAccountTargetRecord{}, gorm.ErrRecordNotFound
}

func TestTargetWorkerDirectedWakeCoalescesAndRetainsWakeDuringExecution(t *testing.T) {
	targets := &logicalTargetStoreStub{}
	started := make(chan string, 8)
	release := make(chan struct{}, 8)
	worker := &TargetWorker{Store: targets, Interval: time.Hour, Executor: targetConvergeFunc(func(ctx context.Context, space, logical string) (targetapp.Result, error) {
		started <- space + "/" + logical
		select {
		case <-release:
			return targetapp.Result{}, nil
		case <-ctx.Done():
			return targetapp.Result{}, ctx.Err()
		}
	})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	t.Cleanup(func() { cancel(); require.ErrorIs(t, <-done, context.Canceled) })
	require.Eventually(t, func() bool { return worker.Snapshot().Ready }, time.Second, time.Millisecond)
	targets.mu.Lock()
	targets.records = []store.LogicalAccountTargetRecord{
		{SpaceID: "space", LogicalAccountID: "one", Status: targetapp.StatusPending},
		{SpaceID: "other-space", LogicalAccountID: "one", Status: targetapp.StatusPending},
	}
	targets.mu.Unlock()
	worker.WakeTarget("space", "one")
	select {
	case got := <-started:
		require.Equal(t, "space/one", got)
	case <-time.After(time.Second):
		t.Fatal("directed wake was lost")
	}
	for range 100 {
		worker.WakeTarget("space", "one")
	}
	release <- struct{}{}
	select {
	case got := <-started:
		require.Equal(t, "space/one", got)
	case <-time.After(time.Second):
		t.Fatal("wake during execution was lost")
	}
	release <- struct{}{}
	select {
	case got := <-started:
		t.Fatalf("unexpected extra target execution: %s", got)
	case <-time.After(30 * time.Millisecond):
	}
	worker.WakeTarget("space", "missing")
	require.Never(t, func() bool { return !worker.Snapshot().Ready }, 30*time.Millisecond, time.Millisecond)
}

func TestTargetWorkerPeriodicScanRecoversUnsignaledTargets(t *testing.T) {
	targets := &logicalTargetStoreStub{}
	converger := &logicalTargetConvergerStub{wake: make(chan struct{}, 4)}
	worker := &TargetWorker{Store: targets, Executor: converger, Interval: 20 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	t.Cleanup(func() { cancel(); require.ErrorIs(t, <-done, context.Canceled) })
	require.Eventually(t, func() bool { return worker.Snapshot().Ready }, time.Second, time.Millisecond)
	targets.mu.Lock()
	targets.records = []store.LogicalAccountTargetRecord{{SpaceID: "space", LogicalAccountID: "unsignaled"}}
	targets.mu.Unlock()
	select {
	case <-converger.wake:
	case <-time.After(time.Second):
		t.Fatal("periodic recovery did not run")
	}
}

func TestTargetWorkerDirectedWakeRepairsExpiredTargetsAndPreservesOtherDiagnostics(t *testing.T) {
	targets := &logicalTargetStoreStub{records: []store.LogicalAccountTargetRecord{
		{SpaceID: "space", LogicalAccountID: "expired", Status: targetapp.StatusExpired},
		{SpaceID: "space", LogicalAccountID: "healthy", Status: targetapp.StatusPending},
	}}
	converger := &logicalTargetConvergerStub{}
	previous := TargetFailure{SpaceID: "space", LogicalAccountID: "other", Error: "offline"}
	worker := &TargetWorker{Store: targets, Executor: converger, targetErrors: []TargetFailure{previous}}
	require.NoError(t, worker.runTargets(context.Background(), []targetKey{
		{"space", "missing"}, {"space", "expired"}, {"space", "healthy"},
	}))
	require.Equal(t, []string{"space/expired", "space/healthy"}, converger.calls)
	require.Equal(t, []TargetFailure{previous}, worker.Snapshot().TargetErrors)
}

func TestTargetWorkerPeriodicScanIncludesExpiredTargetsForRecovery(t *testing.T) {
	targets := &logicalTargetStoreStub{records: []store.LogicalAccountTargetRecord{{
		SpaceID: "space", LogicalAccountID: "expired", Status: targetapp.StatusExpired,
	}}}
	converger := &logicalTargetConvergerStub{wake: make(chan struct{}, 1)}
	worker := &TargetWorker{Store: targets, Executor: converger}
	require.NoError(t, worker.runOnce(context.Background()))
	require.Equal(t, []string{"space/expired"}, converger.calls)
	require.Contains(t, targets.statuses, targetapp.StatusExpired)
}

func TestTargetWorkerDirectedCancellationStopsQueuedCandidates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	worker := &TargetWorker{
		Store: &logicalTargetStoreStub{records: []store.LogicalAccountTargetRecord{
			{SpaceID: "space", LogicalAccountID: "first", Status: targetapp.StatusPending},
			{SpaceID: "space", LogicalAccountID: "second", Status: targetapp.StatusPending},
		}},
		Executor: targetConvergeFunc(func(context.Context, string, string) (targetapp.Result, error) {
			calls++
			cancel()
			return targetapp.Result{}, context.Canceled
		}),
	}
	require.ErrorIs(t, worker.runTargets(ctx, []targetKey{{"space", "first"}, {"space", "second"}}), context.Canceled)
	require.Equal(t, 1, calls)
}

func TestTargetWorkerDirectedWakePreservesAccountLookupDiagnostics(t *testing.T) {
	worker := &TargetWorker{
		Store: &logicalTargetStoreStub{records: []store.LogicalAccountTargetRecord{
			{SpaceID: "space", LogicalAccountID: "logical", Status: targetapp.StatusPending},
		}},
		Executor: targetConvergeFunc(func(context.Context, string, string) (targetapp.Result, error) {
			return targetapp.Result{}, &targetapp.AccountError{TradingAccountID: "account", Err: gorm.ErrRecordNotFound}
		}),
	}
	require.NoError(t, worker.runTargets(context.Background(), []targetKey{{"space", "logical"}}))
	require.Len(t, worker.Snapshot().TargetErrors, 1)
	require.Equal(t, "account", worker.Snapshot().TargetErrors[0].TradingAccountID)
}

func TestTargetWorkerDirectedSuccessPreservesFullScanFailureUntilFullRecovery(t *testing.T) {
	targets := &logicalTargetStoreStub{err: errors.New("invalid target JSON")}
	converger := &logicalTargetConvergerStub{wake: make(chan struct{}, 8)}
	worker := &TargetWorker{Store: targets, Executor: converger, Interval: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	t.Cleanup(func() { cancel(); require.ErrorIs(t, <-done, context.Canceled) })
	require.Eventually(t, func() bool { return worker.Snapshot().LastError == "invalid target JSON" }, time.Second, time.Millisecond)
	targets.mu.Lock()
	targets.err = nil
	targets.records = []store.LogicalAccountTargetRecord{{SpaceID: "space", LogicalAccountID: "healthy", Status: targetapp.StatusPending}}
	targets.mu.Unlock()
	worker.WakeTarget("space", "healthy")
	select {
	case <-converger.wake:
	case <-time.After(time.Second):
		t.Fatal("directed execution did not run")
	}
	require.Never(t, func() bool { return worker.Snapshot().Ready }, 30*time.Millisecond, time.Millisecond)
	require.Equal(t, "invalid target JSON", worker.Snapshot().LastError)
	worker.Wake()
	require.Eventually(t, func() bool { return worker.Snapshot().Ready }, time.Second, time.Millisecond)
	require.Empty(t, worker.Snapshot().LastError)
}

func TestTargetWorkerEmptyDirectedSignalDoesNotClearDirectedFailure(t *testing.T) {
	targets := &logicalTargetStoreStub{}
	worker := &TargetWorker{Store: targets, Interval: time.Hour, Executor: targetConvergeFunc(func(context.Context, string, string) (targetapp.Result, error) {
		return targetapp.Result{}, errors.New("database unavailable")
	})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	t.Cleanup(func() { cancel(); require.ErrorIs(t, <-done, context.Canceled) })
	require.Eventually(t, func() bool { return worker.Snapshot().Ready }, time.Second, time.Millisecond)
	targets.mu.Lock()
	targets.records = []store.LogicalAccountTargetRecord{{SpaceID: "space", LogicalAccountID: "broken", Status: targetapp.StatusPending}}
	targets.mu.Unlock()
	worker.WakeTarget("space", "broken")
	require.Eventually(t, func() bool { return worker.Snapshot().LastError == "database unavailable" }, time.Second, time.Millisecond)
	worker.targetWake <- struct{}{}
	require.Never(t, func() bool { return worker.Snapshot().Ready }, 30*time.Millisecond, time.Millisecond)
	require.Equal(t, "database unavailable", worker.Snapshot().LastError)
}

func (s *logicalTargetStoreStub) ListLogicalAccountTargets(
	_ context.Context,
	statuses ...string,
) ([]store.LogicalAccountTargetRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statuses = append([]string(nil), statuses...)
	return append([]store.LogicalAccountTargetRecord(nil), s.records...), s.err
}

type logicalTargetConvergerStub struct {
	mu     sync.Mutex
	calls  []string
	wake   chan struct{}
	result targetapp.Result
}

func (s *logicalTargetConvergerStub) Converge(
	_ context.Context,
	spaceID string,
	logicalAccountID string,
) (targetapp.Result, error) {
	s.mu.Lock()
	s.calls = append(s.calls, spaceID+"/"+logicalAccountID)
	s.mu.Unlock()
	if s.wake != nil {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
	return s.result, nil
}

type targetMetricsStub struct {
	runs []string
}

func (s *targetMetricsStub) ObserveRun(
	stage string,
	result string,
	healthCheck string,
	_ time.Time,
) error {
	s.runs = append(s.runs, stage+"/"+result+"/"+healthCheck)
	return nil
}

func TestTargetWorkerRunsLogicalAccountsInStableOrder(t *testing.T) {
	targets := &logicalTargetStoreStub{records: []store.LogicalAccountTargetRecord{
		{SpaceID: "space-1", LogicalAccountID: "logical-1", Status: targetapp.StatusPending},
		{SpaceID: "space-1", LogicalAccountID: "logical-2", Status: targetapp.StatusBlocked},
	}}
	converger := &logicalTargetConvergerStub{
		result: targetapp.Result{Status: targetapp.StatusConverged},
	}
	metrics := &targetMetricsStub{}
	worker := &TargetWorker{Store: targets, Executor: converger, Metrics: metrics}

	require.NoError(t, worker.runOnce(context.Background()))
	require.Equal(t, []string{
		"space-1/logical-1",
		"space-1/logical-2",
	}, converger.calls)
	require.Equal(t, []string{
		"target_commit/success/trade-rebalance",
		"target_commit/success/trade-rebalance",
	}, metrics.runs)
	require.Contains(t, targets.statuses, targetapp.StatusConverged)
}

func TestTargetWorkerReportsAcceptedAndRejectedOutcomes(t *testing.T) {
	metrics := &targetMetricsStub{}
	worker := &TargetWorker{Metrics: metrics}
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)

	worker.observe(targetapp.Result{Status: targetapp.StatusConverging}, nil, now)
	worker.observe(targetapp.Result{Status: targetapp.StatusBlocked}, nil, now)
	worker.observe(targetapp.Result{}, errors.New("failed"), now)

	require.Equal(t, []string{
		"target_commit/success/trade-rebalance",
		"target_commit/rejected/trade-rebalance",
		"target_commit/error/trade-rebalance",
	}, metrics.runs)
}

func TestTargetWorkerWakeIsCoalescedAndCancellationStopsRun(t *testing.T) {
	targets := &logicalTargetStoreStub{records: []store.LogicalAccountTargetRecord{{
		SpaceID: "space-1", LogicalAccountID: "logical-1",
		Status: targetapp.StatusPending,
	}}}
	converger := &logicalTargetConvergerStub{wake: make(chan struct{}, 4)}
	worker := &TargetWorker{
		Store: targets, Executor: converger, Interval: time.Hour,
	}
	worker.Wake()
	worker.Wake()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()

	select {
	case <-converger.wake:
	case <-time.After(time.Second):
		t.Fatal("TargetWorker did not run")
	}
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	require.False(t, worker.Snapshot().Ready, "an exited worker must not remain ready")
}

func TestTargetWorkerReadinessRecoversAfterTransientStoreError(t *testing.T) {
	targets := &logicalTargetStoreStub{err: errors.New("temporary list failure")}
	worker := &TargetWorker{
		Store: targets, Executor: &logicalTargetConvergerStub{},
		Interval: 10 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()

	require.Eventually(t, func() bool {
		snapshot := worker.Snapshot()
		return !snapshot.Ready && snapshot.LastError == "temporary list failure"
	}, time.Second, 10*time.Millisecond)
	targets.mu.Lock()
	targets.err = nil
	targets.mu.Unlock()
	require.Eventually(t, func() bool {
		snapshot := worker.Snapshot()
		return snapshot.Ready && snapshot.LastError == ""
	}, time.Second, 10*time.Millisecond)

	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
}

type targetConvergeFunc func(context.Context, string, string) (targetapp.Result, error)

func (f targetConvergeFunc) Converge(ctx context.Context, space, logical string) (targetapp.Result, error) {
	return f(ctx, space, logical)
}

func TestTargetWorkerBoundsSlowCandidateAndContinues(t *testing.T) {
	var calls []string
	worker := &TargetWorker{
		Store: &logicalTargetStoreStub{records: []store.LogicalAccountTargetRecord{
			{SpaceID: "space", LogicalAccountID: "slow"},
			{SpaceID: "space", LogicalAccountID: "healthy"},
		}},
		ConvergeTimeout: 20 * time.Millisecond,
		Executor: targetConvergeFunc(func(ctx context.Context, _, logical string) (targetapp.Result, error) {
			calls = append(calls, logical)
			if logical == "slow" {
				if _, ok := ctx.Deadline(); !ok {
					return targetapp.Result{}, errors.New("no candidate deadline")
				}
				<-ctx.Done()
				return targetapp.Result{}, ctx.Err()
			}
			return targetapp.Result{Status: targetapp.StatusConverged}, nil
		}),
	}
	require.ErrorIs(t, worker.runOnce(context.Background()), context.DeadlineExceeded)
	require.Equal(t, []string{"slow", "healthy"}, calls)
}

func TestTargetWorkerParentCancellationStopsRemainingCandidates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	worker := &TargetWorker{
		Store: &logicalTargetStoreStub{records: []store.LogicalAccountTargetRecord{
			{SpaceID: "space", LogicalAccountID: "first"},
			{SpaceID: "space", LogicalAccountID: "second"},
		}},
		Executor: targetConvergeFunc(func(context.Context, string, string) (targetapp.Result, error) {
			calls++
			cancel()
			return targetapp.Result{}, context.Canceled
		}),
	}
	require.ErrorIs(t, worker.runOnce(ctx), context.Canceled)
	require.Equal(t, 1, calls)
}

func TestTargetWorkerAccountErrorIsDiagnosticAndRecovers(t *testing.T) {
	bad := true
	worker := &TargetWorker{
		Store: &logicalTargetStoreStub{records: []store.LogicalAccountTargetRecord{
			{SpaceID: "space", LogicalAccountID: "logical", TargetID: "target"},
		}},
		Executor: targetConvergeFunc(func(context.Context, string, string) (targetapp.Result, error) {
			if bad {
				return targetapp.Result{}, &targetapp.AccountError{TradingAccountID: "account", Err: errors.New("quote unavailable")}
			}
			return targetapp.Result{Status: targetapp.StatusConverged}, nil
		}),
	}
	require.NoError(t, worker.runOnce(context.Background()))
	snapshot := worker.Snapshot()
	require.Equal(t, []TargetFailure{{SpaceID: "space", LogicalAccountID: "logical", TargetID: "target", TradingAccountID: "account", Error: "target account account: quote unavailable"}}, snapshot.TargetErrors)
	snapshot.TargetErrors[0].Error = "changed by caller"
	require.NotEqual(t, "changed by caller", worker.Snapshot().TargetErrors[0].Error)
	bad = false
	require.NoError(t, worker.runOnce(context.Background()))
	require.Empty(t, worker.Snapshot().TargetErrors)
}
