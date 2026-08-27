package eventconsumer

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/trigger"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	storagepb "github.com/mooyang-code/moox/packages/storagepb"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type fakeNATSSession struct {
	run        func(context.Context) error
	closeCalls atomic.Int32
}

func (s *fakeNATSSession) Run(ctx context.Context) error { return s.run(ctx) }
func (s *fakeNATSSession) Close() error {
	s.closeCalls.Add(1)
	return nil
}

type recordingExecutor struct {
	calls atomic.Int32
	err   error
}

func (e *recordingExecutor) Execute(context.Context, string, string, *storagepb.ViewSourcePeriodReady) error {
	e.calls.Add(1)
	return e.err
}

type blockingExecutor struct {
	calls atomic.Int32
}

func (e *blockingExecutor) Execute(ctx context.Context, _ string, _ string, _ *storagepb.ViewSourcePeriodReady) error {
	e.calls.Add(1)
	<-ctx.Done()
	return ctx.Err()
}

func TestConsumerReopensFailedSessionAndRestoresReadiness(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	release := make(chan struct{})
	recovered := make(chan struct{})
	first := &fakeNATSSession{run: func(context.Context) error {
		close(started)
		<-release
		return errors.New("fetch failed")
	}}
	second := &fakeNATSSession{run: func(ctx context.Context) error {
		close(recovered)
		<-ctx.Done()
		return nil
	}}
	var opens atomic.Int32
	consumer := New(Config{}, &recordingExecutor{})
	consumer.retryDelay = 10 * time.Millisecond
	consumer.openSession = func(context.Context) (natsConsumerSession, error) {
		opens.Add(1)
		return second, nil
	}
	consumer.startSessionLoop(ctx, first)
	t.Cleanup(func() { _ = consumer.Close() })

	<-started
	require.True(t, consumer.Ready())
	close(release)
	require.Eventually(t, func() bool { return !consumer.Ready() }, time.Second, time.Millisecond)
	require.Eventually(t, consumer.Ready, time.Second, time.Millisecond)
	<-recovered
	require.Equal(t, int32(1), opens.Load())
	require.Equal(t, int32(1), first.closeCalls.Load())
}

func TestViewReadyConsumerConfig(t *testing.T) {
	ref := viewReadyConsumerConfig(Config{FetchMaxWait: 2 * time.Second, MaxExecutionAttempts: 5})
	require.Equal(t, events.ViewSourcePeriodReady.Name(), ref.Event.Name())
	require.Equal(t, ViewSourceReadyConsumerName, ref.Name)
	require.Equal(t, 2*time.Second, ref.FetchMaxWait)
	require.Equal(t, nats.DeliverNewPolicy, ref.DeliverPolicy)
	require.Equal(t, -1, ref.MaxDeliver)
	require.Equal(t, 16, ref.MaxAckPending)
}

func TestHandlerExecutesViewReadyEvent(t *testing.T) {
	executor := &recordingExecutor{}
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	readyAt := time.Date(2026, 8, 9, 1, 0, 0, 0, time.UTC)
	payload := &storagepb.ViewSourcePeriodReady{SourceViewId: "prices-view", Frequency: "1m", PeriodTime: readyAt.Unix(), Status: "complete", ReadyAt: timestamppb.New(readyAt), Datasets: []*storagepb.ViewPeriodDatasetState{{DatasetId: "prices", Status: "complete"}}}
	encoded, err := registry.Encode(events.ViewSourcePeriodReady, payload, events.PublishOptions{
		EventID: "ready-1", OccurredAt: readyAt, SpaceID: "quant", SubjectID: "prices-view",
	})
	require.NoError(t, err)
	raw, err := proto.Marshal(encoded.Message)
	require.NoError(t, err)
	result := (storageEventHandler{executor: executor}).Handle(context.Background(), &jetstream.Delivery{
		Subject: encoded.Subject, RawData: raw, RawMessageID: "ready-1", ContentType: events.ContentType,
	})
	require.Equal(t, jetstream.ACK, result.Decision)
	require.Equal(t, int32(1), executor.calls.Load())
}

func TestHandlerAcknowledgesNoBindingAndRetriesPendingBinding(t *testing.T) {
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	readyAt := time.Date(2026, 8, 10, 1, 0, 0, 0, time.UTC)
	payload := &storagepb.ViewSourcePeriodReady{
		SourceViewId: "prices-view", Frequency: "1m", PeriodTime: readyAt.Unix(),
		Status: "complete", ReadyAt: timestamppb.New(readyAt),
		Datasets: []*storagepb.ViewPeriodDatasetState{{DatasetId: "prices", Status: "complete"}},
	}
	encoded, err := registry.Encode(events.ViewSourcePeriodReady, payload, events.PublishOptions{
		EventID: "ready-binding-state", OccurredAt: readyAt, SpaceID: "quant", SubjectID: "prices-view",
	})
	require.NoError(t, err)
	raw, err := proto.Marshal(encoded.Message)
	require.NoError(t, err)
	delivery := &jetstream.Delivery{
		Subject: encoded.Subject, RawData: raw, RawMessageID: "ready-binding-state", ContentType: events.ContentType,
	}

	noBinding := (storageEventHandler{executor: &recordingExecutor{err: trigger.ErrNoExecutableBinding}}).
		Handle(context.Background(), delivery)
	require.Equal(t, jetstream.ACK, noBinding.Decision)

	pending := (storageEventHandler{executor: &recordingExecutor{err: trigger.ErrBindingNotReady}}).
		Handle(context.Background(), delivery)
	require.Equal(t, jetstream.RETRY, pending.Decision)
	require.ErrorIs(t, pending.Err, trigger.ErrBindingNotReady)
}

func TestHandlerTerminatesMalformedEvent(t *testing.T) {
	result := (storageEventHandler{executor: &recordingExecutor{}}).Handle(context.Background(), &jetstream.Delivery{
		ContentType: "application/json",
	})
	require.Equal(t, jetstream.TERM, result.Decision)
	require.ErrorContains(t, result.Err, "factor event rejected")
}

func TestHandlerTimesOutOneExecution(t *testing.T) {
	executor := &blockingExecutor{}
	delivery := encodedViewReadyDelivery(t, "ready-timeout")
	started := time.Now()
	result := (storageEventHandler{
		executor:             executor,
		executionTimeout:     20 * time.Millisecond,
		maxExecutionAttempts: 5,
	}).Handle(context.Background(), delivery)

	require.Equal(t, jetstream.RETRY, result.Decision)
	require.ErrorIs(t, result.Err, context.DeadlineExceeded)
	require.Less(t, time.Since(started), time.Second)
	require.Equal(t, int32(1), executor.calls.Load())
}

func TestHandlerQuarantinesTimedOutExecutionWithoutDroppingIt(t *testing.T) {
	delivery := encodedViewReadyDelivery(t, "ready-poison")
	delivery.DeliveryCount = 1
	handler := storageEventHandler{
		executor:             &recordingExecutor{err: context.DeadlineExceeded},
		maxExecutionAttempts: 5,
		progress:             newProgressState(),
	}
	for range 4 {
		result := handler.Handle(context.Background(), delivery)
		require.Equal(t, jetstream.RETRY, result.Decision)
	}
	result := handler.Handle(context.Background(), delivery)

	require.Equal(t, jetstream.RETRY, result.Decision)
	require.GreaterOrEqual(t, result.Delay, 30*time.Second)
	require.ErrorContains(t, result.Err, "retry threshold reached")
	require.True(t, handler.progress.status(time.Now(), 5*time.Minute).Stalled)
}

func TestHandlerTracksInterleavedTimeoutAttemptsPerEvent(t *testing.T) {
	handler := storageEventHandler{
		executor:             &recordingExecutor{err: context.DeadlineExceeded},
		maxExecutionAttempts: 3,
		progress:             newProgressState(),
	}
	eventA := encodedViewReadyDelivery(t, "ready-timeout-a")
	eventB := encodedViewReadyDelivery(t, "ready-timeout-b")

	for _, delivery := range []*jetstream.Delivery{eventA, eventB, eventA, eventB} {
		result := handler.Handle(context.Background(), delivery)
		require.Equal(t, jetstream.RETRY, result.Decision)
		require.Less(t, result.Delay, 30*time.Second)
	}

	result := handler.Handle(context.Background(), eventA)
	require.Equal(t, jetstream.RETRY, result.Decision)
	require.GreaterOrEqual(t, result.Delay, 30*time.Second)
	require.ErrorContains(t, result.Err, "retry threshold reached")
}

func TestHandlerKeepsRetryingRecoverableFailureWithoutDataLoss(t *testing.T) {
	delivery := encodedViewReadyDelivery(t, "ready-storage-retry")
	delivery.DeliveryCount = 50
	result := (storageEventHandler{
		executor:             &recordingExecutor{err: errors.New("storage unavailable")},
		maxExecutionAttempts: 5,
	}).Handle(context.Background(), delivery)

	require.Equal(t, jetstream.RETRY, result.Decision)
	require.ErrorContains(t, result.Err, "storage unavailable")
}

func TestProgressDetectsInFlightExecutionPastThreshold(t *testing.T) {
	progress := newProgressState()
	executor := &blockingExecutor{}
	handler := storageEventHandler{
		executor: executor, executionTimeout: 100 * time.Millisecond,
		maxExecutionAttempts: 5, progress: progress,
	}
	delivery := encodedViewReadyDelivery(t, "ready-stalled")
	done := make(chan jetstream.HandlerResult, 1)
	go func() {
		done <- handler.Handle(context.Background(), delivery)
	}()
	require.Eventually(t, func() bool {
		return progress.status(time.Now(), 10*time.Millisecond).Stalled
	}, time.Second, time.Millisecond)
	status := progress.status(time.Now(), 10*time.Millisecond)
	require.Equal(t, "ready-stalled", status.InFlightEventID)
	require.False(t, status.LastReceivedAt.IsZero())

	result := <-done
	require.Equal(t, jetstream.RETRY, result.Decision)
	require.True(t, progress.status(time.Now(), 10*time.Millisecond).Stalled)
	progress.begin("ready-recovered", time.Now())
	require.True(t, progress.status(time.Now(), 10*time.Millisecond).Stalled)
	progress.finish("ready-recovered", true, nil, time.Now())
	require.False(t, progress.status(time.Now(), 10*time.Millisecond).Stalled)
}

func TestProgressRecordsSuccessfulCompletionAndAck(t *testing.T) {
	progress := newProgressState()
	delivery := encodedViewReadyDelivery(t, "ready-ok")
	result := (storageEventHandler{
		executor: &recordingExecutor{}, maxExecutionAttempts: 5, progress: progress,
	}).Handle(context.Background(), delivery)
	require.Equal(t, jetstream.ACK, result.Decision)
	progress.ReportAction(context.Background(), delivery, result, nil)

	status := progress.status(time.Now(), time.Minute)
	require.False(t, status.LastCompletedAt.IsZero())
	require.False(t, status.LastAckAt.IsZero())
	require.Empty(t, status.InFlightEventID)
}

func TestProgressRecordsRejectedMessageFailure(t *testing.T) {
	progress := newProgressState()
	result := jetstream.HandlerResult{Decision: jetstream.TERM, Err: errors.New("invalid payload")}
	progress.ReportAction(context.Background(), &jetstream.Delivery{}, result, nil)

	status := progress.status(time.Now(), time.Minute)
	require.ErrorContains(t, errors.New(status.LastFailure), "invalid payload")
	require.False(t, status.LastFailureAt.IsZero())
}

func encodedViewReadyDelivery(t *testing.T, eventID string) *jetstream.Delivery {
	t.Helper()
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	readyAt := time.Date(2026, 8, 10, 1, 0, 0, 0, time.UTC)
	payload := &storagepb.ViewSourcePeriodReady{
		SourceViewId: "prices-view", Frequency: "1m", PeriodTime: readyAt.Unix(),
		Status: "complete", ReadyAt: timestamppb.New(readyAt),
		Datasets: []*storagepb.ViewPeriodDatasetState{{DatasetId: "prices", Status: "complete"}},
	}
	encoded, err := registry.Encode(events.ViewSourcePeriodReady, payload, events.PublishOptions{
		EventID: eventID, OccurredAt: readyAt, SpaceID: "quant", SubjectID: "prices-view",
	})
	require.NoError(t, err)
	raw, err := proto.Marshal(encoded.Message)
	require.NoError(t, err)
	return &jetstream.Delivery{
		Subject: encoded.Subject, RawData: raw, RawMessageID: eventID,
		ContentType: events.ContentType, DeliveryCount: 1,
	}
}
