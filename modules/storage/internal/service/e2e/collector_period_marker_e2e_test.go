//go:build cgo

package e2e

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/outbox"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	storagegen "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/jetstream/testkit"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestCollectorPeriodMarkerJetStreamAckE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	period := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	store, err := pebble.Open(pebble.Options{
		Path: filepath.Join(t.TempDir(), "pebble"), NodeID: "period-marker-e2e",
		PeriodNow: func() time.Time { return period.Add(5 * time.Second) },
	})
	require.NoError(t, err)
	defer store.Close()

	bus := testkit.Start(t)
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	const (
		stream  = "MOOX_STORAGE"
		durable = "collector-period-marker-e2e"
	)
	const storageSubject = "moox.event.storage.>"
	bus.AddStream(t, &nats.StreamConfig{Name: stream, Subjects: []string{storageSubject}, Storage: nats.MemoryStorage, Retention: nats.LimitsPolicy})
	markerSubject, err := registry.RenderSubject(events.CollectorPeriodCompleted, "crypto", "dataset-period-marker-e2e")
	require.NoError(t, err)
	_, err = bus.JetStream().AddConsumer(stream, &nats.ConsumerConfig{
		Name: durable, Durable: durable, FilterSubject: markerSubject,
		AckPolicy: nats.AckExplicitPolicy, AckWait: time.Second, MaxAckPending: 1,
	})
	require.NoError(t, err)
	subscriber, err := bus.JetStream().PullSubscribe(markerSubject, durable, nats.BindStream(stream), nats.ManualAck())
	require.NoError(t, err)
	defer subscriber.Unsubscribe()

	eventClient, err := jetstream.Connect(ctx, jetstream.Config{URLs: []string{bus.URL()}, Name: "storage-period-marker-relay-e2e"})
	require.NoError(t, err)
	defer eventClient.Close()
	relayErrors := make(chan error, 1)
	publisher := &gatedPeriodMarkerPublisher{
		inner:   outbox.NewJetStreamPublisher(eventClient),
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	relay, err := outbox.NewRelay(store, publisher, outbox.RelayOptions{
		PollInterval: 10 * time.Millisecond,
		BatchSize:    1,
		ErrorReporter: func(err error) {
			select {
			case relayErrors <- err:
			default:
			}
		},
	})
	require.NoError(t, err)
	relay.Start(ctx)
	defer func() {
		publisher.releasePublish()
		relay.Close()
	}()

	deadline := period.Add(time.Minute)
	expectation := pebble.DatasetPeriodExpectation{
		SpaceID: "crypto", DatasetID: "dataset-period-marker-e2e", Frequency: "1m",
		PeriodTime: period.Unix(), SeriesHash: "period-marker-e2e-scope", ExpectedCount: 2,
		DeadlineAt: deadline.Unix(),
		SeriesSnapshot: []pebble.DatasetPeriodSeries{
			{SeriesIndex: 0, SubjectID: "BTC-USDT"},
			{SeriesIndex: 1, SubjectID: "ETH-USDT"},
		},
	}
	_, err = store.EnsureDatasetPeriod(ctx, expectation)
	require.NoError(t, err)
	row := &storagegen.RowFieldUpsert{
		Key: &storagegen.RowKey{SpaceId: expectation.SpaceID, DatasetId: expectation.DatasetID, Kind: &storagegen.RowKey_TimeSeries{TimeSeries: &storagegen.TimeSeriesRowKey{
			SubjectId: "BTC-USDT", Freq: expectation.Frequency, DataTime: period.Format(time.RFC3339Nano),
		}}},
		Fields: []*storagegen.FieldValue{{FieldId: "close", Value: &storagegen.TypedValue{Value: &storagegen.TypedValue_DoubleValue{DoubleValue: 68000}}}},
	}
	_, err = store.CommitTimeSeriesBatch(ctx, expectation, []pebble.TimeSeriesBatchItem{{SeriesIndex: 0, Row: row}}, "period-marker-e2e-row", "collector")
	require.NoError(t, err)
	failures, err := store.RecordDatasetPeriodFailures(ctx, expectation, []uint32{1})
	require.NoError(t, err)
	require.Equal(t, "waiting", failures.Status)
	require.Len(t, failures.FailureResults, 1)
	require.Equal(t, storagegen.PeriodFailureDisposition_PERIOD_FAILURE_DISPOSITION_RECORDED, failures.FailureResults[0].GetDisposition())
	finalized, err := store.FinalizeWaitingDatasetPeriods(ctx, deadline, 10)
	require.NoError(t, err)
	require.Equal(t, 1, finalized)
	select {
	case <-publisher.entered:
	case <-ctx.Done():
		require.NoError(t, ctx.Err())
	}

	messages, err := subscriber.Fetch(1, nats.MaxWait(5*time.Second))
	require.NoError(t, err)
	require.Len(t, messages, 1)
	message := messages[0]
	metadata, err := message.Metadata()
	require.NoError(t, err)
	envelope, marker, err := events.DecodeCollectorPeriodCompletedWithContentType(
		registry, message.Data, message.Subject, message.Header.Get(nats.MsgIdHdr), message.Header.Get("Content-Type"),
	)
	require.NoError(t, err)
	require.NotEmpty(t, envelope.GetEventId())
	require.NotEqual(t, "outbox-pending", envelope.GetEventId())
	require.Equal(t, "crypto", envelope.GetSpaceId())
	require.Equal(t, expectation.DatasetID, envelope.GetSubjectId())
	require.Equal(t, expectation.DatasetID, marker.GetDatasetId())
	require.Equal(t, expectation.Frequency, marker.GetFrequency())
	require.Equal(t, expectation.PeriodTime, marker.GetPeriodTime())
	require.Equal(t, "degraded", marker.GetStatus())
	require.Equal(t, []string{"BTC-USDT", "ETH-USDT"}, marker.GetUniverseSubjectIds())
	require.Equal(t, []string{"ETH-USDT"}, marker.GetFailedSubjects())
	require.Equal(t, expectation.SeriesHash, marker.GetExpectedScopeRef())
	require.NoError(t, message.AckSync())

	consumerInfo, err := bus.JetStream().ConsumerInfo(stream, durable)
	require.NoError(t, err)
	require.Zero(t, consumerInfo.NumAckPending, "the durable subscriber must acknowledge the delivered marker")
	require.GreaterOrEqual(t, consumerInfo.AckFloor.Stream, metadata.Sequence.Stream)
	entries, err := store.ListOutbox(ctx, 0, 10)
	require.NoError(t, err)
	var markerStillPending bool
	for _, entry := range entries {
		pending := new(eventpb.EventMessage)
		require.NoError(t, proto.Unmarshal(entry.Data, pending))
		if pending.GetEventName() == events.CollectorPeriodCompleted.Name() {
			markerStillPending = true
		}
	}
	require.True(t, markerStillPending, "the marker must remain in the outbox until the relay's publisher call returns success")
	publisher.releasePublish()
	require.Eventually(t, func() bool {
		select {
		case relayErr := <-relayErrors:
			t.Errorf("Storage outbox relay: %v", relayErr)
			return false
		default:
		}
		entries, listErr := store.ListOutbox(ctx, 0, 10)
		return listErr == nil && len(entries) == 0
	}, 5*time.Second, 20*time.Millisecond, "the relay should remove entries only after JetStream publish acknowledgement")
	t.Log("SCENARIO PASS collector-period-marker-jetstream-ack")
}

type gatedPeriodMarkerPublisher struct {
	inner       outbox.Publisher
	entered     chan struct{}
	release     chan struct{}
	enteredOnce sync.Once
	releaseOnce sync.Once
}

func (p *gatedPeriodMarkerPublisher) PublishMessage(ctx context.Context, data []byte) error {
	if err := p.inner.PublishMessage(ctx, data); err != nil {
		return err
	}
	message := new(eventpb.EventMessage)
	if err := proto.Unmarshal(data, message); err != nil {
		return err
	}
	if message.GetEventName() != events.CollectorPeriodCompleted.Name() {
		return nil
	}
	p.enteredOnce.Do(func() { close(p.entered) })
	select {
	case <-p.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *gatedPeriodMarkerPublisher) releasePublish() {
	p.releaseOnce.Do(func() { close(p.release) })
}
