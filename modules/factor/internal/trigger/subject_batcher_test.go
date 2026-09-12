package trigger

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
)

func subjectEvent(id string) SubjectEvent {
	return SubjectEvent{SpaceID: "space", EventID: id, Ready: &storagepb.ViewSourceSubjectReady{
		SourceViewId: "prices", SourceDatasetId: "bars", SubjectId: id, Frequency: "1m", PeriodTime: 60,
		ActiveIndexId: "index-a", InputContractVersion: "schema:1", SourceEventId: id,
		SourceNodeId: "node", SourceStoreId: "store", SourceSequence: 1,
	}}
}

func TestSubjectBatcherCombinesAndWaitsForExecution(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	entered := make(chan []SubjectEvent, 1)
	release := make(chan struct{})
	failure := errors.New("source unavailable")
	b, err := NewSubjectBatcher(SubjectBatchConfig{Window: time.Second, MaxBatch: 2, QueueCapacity: 4, ExecutionTimeout: time.Second}, func(ctx context.Context, batch []SubjectEvent) error {
		entered <- batch
		select {
		case <-release:
			return failure
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()
	results := make(chan error, 2)
	for _, id := range []string{"BTC", "ETH"} {
		go func(id string) { results <- b.Submit(ctx, subjectEvent(id)) }(id)
	}
	select {
	case batch := <-entered:
		require.Len(t, batch, 2)
	case <-ctx.Done():
		t.Fatal("batch did not execute")
	}
	select {
	case <-results:
		t.Fatal("submission completed before execution")
	default:
	}
	close(release)
	require.ErrorIs(t, <-results, failure)
	require.ErrorIs(t, <-results, failure)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	require.ErrorIs(t, b.Submit(context.Background(), subjectEvent("late")), ErrSubjectBatcherStopped)
}

func TestSubjectBatcherFlushesWithoutUniverseAndSeparatesContracts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var mu sync.Mutex
	var batches [][]SubjectEvent
	b, err := NewSubjectBatcher(SubjectBatchConfig{Window: 10 * time.Millisecond, MaxBatch: 4, QueueCapacity: 4, ExecutionTimeout: time.Second}, func(_ context.Context, batch []SubjectEvent) error {
		mu.Lock()
		batches = append(batches, batch)
		mu.Unlock()
		return nil
	})
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()
	require.NoError(t, b.Submit(ctx, subjectEvent("BTC")), "one subject must not wait for the universe")
	results := make(chan error, 2)
	other := subjectEvent("ETH")
	other.Ready.InputContractVersion = "schema:2"
	go func() { results <- b.Submit(ctx, subjectEvent("BTC")) }()
	go func() { results <- b.Submit(ctx, other) }()
	require.NoError(t, <-results)
	require.NoError(t, <-results)
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	for _, batch := range batches {
		for _, event := range batch {
			require.Equal(t, batch[0].Ready.InputContractVersion, event.Ready.InputContractVersion)
		}
	}
}

func TestSubjectBatcherBoundsQueueAndCancelsWaitingSubmitters(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	entered := make(chan struct{}, 1)
	b, err := NewSubjectBatcher(SubjectBatchConfig{Window: time.Millisecond, MaxBatch: 1, QueueCapacity: 1, ExecutionTimeout: time.Second}, func(ctx context.Context, _ []SubjectEvent) error {
		entered <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	})
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()
	result := make(chan error, 1)
	go func() { result <- b.Submit(ctx, subjectEvent("BTC")) }()
	<-entered
	waitCtx, stopWaiting := context.WithTimeout(ctx, 10*time.Millisecond)
	defer stopWaiting()
	require.ErrorIs(t, b.Submit(waitCtx, subjectEvent("ETH")), context.DeadlineExceeded)
	require.Len(t, b.queue, 1)
	fullCtx, stopFull := context.WithTimeout(ctx, 10*time.Millisecond)
	defer stopFull()
	require.ErrorIs(t, b.Submit(fullCtx, subjectEvent("SOL")), context.DeadlineExceeded)
	require.Len(t, b.queue, 1, "queue must not grow while execution is blocked")
	cancel()
	require.ErrorIs(t, <-result, context.Canceled)
	require.ErrorIs(t, <-done, context.Canceled)
}

func TestSubjectBatcherRejectsInvalidConfigurationAndEvents(t *testing.T) {
	_, err := NewSubjectBatcher(SubjectBatchConfig{}, nil)
	require.Error(t, err)
	b, err := NewSubjectBatcher(SubjectBatchConfig{Window: time.Millisecond, MaxBatch: 1, QueueCapacity: 1, ExecutionTimeout: time.Second}, func(context.Context, []SubjectEvent) error { return nil })
	require.NoError(t, err)
	require.Error(t, b.Submit(context.Background(), SubjectEvent{}))
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, b.Run(cancelled), context.Canceled)
	require.Error(t, b.Run(context.Background()))
}

func TestSubjectBatcherSkipsGroupCancelledWhileEarlierGroupExecutes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	calls := 0
	b, err := NewSubjectBatcher(DefaultSubjectBatchConfig(), func(context.Context, []SubjectEvent) error {
		calls++
		if calls == 1 {
			close(entered)
			<-release
		}
		return nil
	})
	require.NoError(t, err)
	secondCtx, cancelSecond := context.WithCancel(ctx)
	defer cancelSecond()
	other := subjectEvent("ETH")
	other.Ready.ActiveIndexId = "index-b"
	first := subjectSubmission{ctx: ctx, event: subjectEvent("BTC"), done: make(chan error, 1)}
	second := subjectSubmission{ctx: secondCtx, event: other, done: make(chan error, 1)}
	done := make(chan struct{})
	go func() { b.runGroups(ctx, []subjectSubmission{first, second}); close(done) }()
	<-entered
	cancelSecond()
	close(release)
	<-done
	require.Equal(t, 1, calls)
	require.NoError(t, <-first.done)
	require.ErrorIs(t, <-second.done, context.Canceled)
}
