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
	ref := viewReadyConsumerConfig(Config{FetchMaxWait: 2 * time.Second})
	require.Equal(t, events.ViewSourcePeriodReady.Name(), ref.Event.Name())
	require.Equal(t, ViewSourceReadyConsumerName, ref.Name)
	require.Equal(t, 2*time.Second, ref.FetchMaxWait)
	require.Equal(t, nats.DeliverNewPolicy, ref.DeliverPolicy)
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
