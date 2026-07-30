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
)

type logicalTargetStoreStub struct {
	mu       sync.Mutex
	records  []store.LogicalAccountTargetRecord
	statuses []string
	err      error
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

func TestTargetWorkerGateSerializesTargetAcceptance(t *testing.T) {
	targets := &logicalTargetStoreStub{records: []store.LogicalAccountTargetRecord{{
		SpaceID: "space-1", LogicalAccountID: "logical-1",
		Status: targetapp.StatusPending,
	}}}
	converger := &logicalTargetConvergerStub{wake: make(chan struct{}, 1)}
	gate := &sync.Mutex{}
	gate.Lock()
	worker := &TargetWorker{Store: targets, Executor: converger, Gate: gate}
	done := make(chan error, 1)
	go func() { done <- worker.runOnce(context.Background()) }()

	select {
	case <-converger.wake:
		t.Fatal("target execution crossed the acceptance gate")
	case <-time.After(20 * time.Millisecond):
	}
	gate.Unlock()
	require.NoError(t, <-done)
	require.Equal(t, []string{"space-1/logical-1"}, converger.calls)
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
