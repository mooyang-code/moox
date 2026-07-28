package runtime

import (
	"context"
	"sync"
	"testing"
	"time"

	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

type targetExecutionStoreStub struct {
	mu      sync.Mutex
	records []store.TargetExecutionRecord
	updates []store.TargetExecutionRecord
}

func (s *targetExecutionStoreStub) ListTargetExecutions(
	context.Context,
	...string,
) ([]store.TargetExecutionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]store.TargetExecutionRecord(nil), s.records...), nil
}

func (s *targetExecutionStoreStub) UpdateTargetExecutionState(
	_ context.Context,
	record store.TargetExecutionRecord,
) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates = append(s.updates, record)
	return true, nil
}

type targetConvergerStub struct {
	mu    sync.Mutex
	calls []string
	wake  chan struct{}
}

func (s *targetConvergerStub) Converge(
	_ context.Context,
	spaceID string,
	bindingID string,
) (targetapp.Result, error) {
	s.mu.Lock()
	s.calls = append(s.calls, spaceID+"/"+bindingID)
	s.mu.Unlock()
	if s.wake != nil {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
	return targetapp.Result{Status: targetapp.StatusRunning}, nil
}

func TestTargetWorkerRunsActiveExecutionsInStableOrder(t *testing.T) {
	executions := &targetExecutionStoreStub{records: []store.TargetExecutionRecord{
		targetExecutionRecord("space-1", "binding-1", "account-1", "BTC-USDT"),
		targetExecutionRecord("space-1", "binding-2", "account-1", "ETH-USDT"),
	}}
	converger := &targetConvergerStub{}
	worker := &TargetWorker{Store: executions, Executor: converger}

	require.NoError(t, worker.runOnce(context.Background()))
	require.Equal(t, []string{"space-1/binding-1", "space-1/binding-2"}, converger.calls)
	require.Empty(t, executions.updates)
}

func TestTargetWorkerPausesCompetingBindingsOnSameLane(t *testing.T) {
	executions := &targetExecutionStoreStub{records: []store.TargetExecutionRecord{
		targetExecutionRecord("space-1", "binding-1", "account-1", "BTC-USDT"),
		targetExecutionRecord("space-1", "binding-2", "account-1", "BTC-USDT"),
	}}
	converger := &targetConvergerStub{}
	worker := &TargetWorker{Store: executions, Executor: converger}

	require.NoError(t, worker.runOnce(context.Background()))
	require.Empty(t, converger.calls)
	require.Len(t, executions.updates, 2)
	for _, updated := range executions.updates {
		require.Equal(t, targetapp.StatusPaused, updated.Status)
		require.Contains(t, updated.LastError, "multiple active execution bindings")
	}
}

func TestTargetWorkerLetsExpiredConflictsTerminate(t *testing.T) {
	now := time.UnixMilli(10_000)
	first := targetExecutionRecord("space-1", "binding-1", "account-1", "BTC-USDT")
	second := targetExecutionRecord("space-1", "binding-2", "account-1", "BTC-USDT")
	first.NotAfter = now.Add(-time.Second).UnixMilli()
	second.NotAfter = now.Add(-time.Second).UnixMilli()
	executions := &targetExecutionStoreStub{
		records: []store.TargetExecutionRecord{first, second},
	}
	converger := &targetConvergerStub{}
	worker := &TargetWorker{
		Store: executions, Executor: converger,
		Now: func() time.Time { return now },
	}

	require.NoError(t, worker.runOnce(context.Background()))
	require.Equal(
		t,
		[]string{"space-1/binding-1", "space-1/binding-2"},
		converger.calls,
	)
	require.Empty(t, executions.updates)
}

func TestTargetWorkerWakeIsCoalescedAndCancellationStopsRun(t *testing.T) {
	executions := &targetExecutionStoreStub{records: []store.TargetExecutionRecord{
		targetExecutionRecord("space-1", "binding-1", "account-1", "BTC-USDT"),
	}}
	converger := &targetConvergerStub{wake: make(chan struct{}, 4)}
	worker := &TargetWorker{
		Store: executions, Executor: converger, Interval: time.Hour,
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
}

func targetExecutionRecord(
	spaceID string,
	bindingID string,
	accountID string,
	symbol string,
) store.TargetExecutionRecord {
	return store.TargetExecutionRecord{
		SpaceID: spaceID, ExecutionID: "execution-" + bindingID,
		ExecutionBindingID: bindingID, ExchangeAccountID: accountID,
		CommandSequence: 1, Status: targetapp.StatusRunning,
		NotAfter: time.Now().Add(time.Hour).UnixMilli(),
		Targets: []store.TargetPosition{{
			InstrumentID: symbol, Symbol: symbol, TargetQuantity: "1",
		}},
	}
}
