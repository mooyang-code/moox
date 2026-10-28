package eventconsumer

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/jetstream/testkit"
	"github.com/mooyang-code/moox/packages/storagepb"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type fakeTriggers struct {
	mu         sync.Mutex
	periods    []string
	backfills  []string
	periodErrs int
}

func (f *fakeTriggers) OnPeriod(_ context.Context, spaceID string, payload *storagepb.DatasetPeriodCompleted) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.periodErrs > 0 {
		f.periodErrs--
		return errors.New("临时失败")
	}
	f.periods = append(f.periods, spaceID+"/"+payload.GetDatasetId())
	return nil
}

func (f *fakeTriggers) OnBackfilled(_ context.Context, spaceID, eventID string, payload *storagepb.CollectorKlineBackfilled) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.backfills = append(f.backfills, spaceID+"/"+eventID+"/"+payload.GetDatasetId())
	return nil
}

func (f *fakeTriggers) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.periods), len(f.backfills)
}

func startBus(t *testing.T) *testkit.Server {
	t.Helper()
	server := testkit.Start(t)
	server.AddStream(t, &nats.StreamConfig{Name: "MOOX_STORAGE", Subjects: []string{"moox.event.storage.dataset.period.completed.v1.>"}, Storage: nats.MemoryStorage})
	server.AddStream(t, &nats.StreamConfig{Name: "MOOX_COLLECTOR", Subjects: []string{"moox.event.collector.kline.backfilled.v1.>"}, Storage: nats.MemoryStorage})
	return server
}

func publisherFor(t *testing.T, server *testkit.Server, registry *events.Registry) *events.Publisher {
	t.Helper()
	client, err := jetstream.Connect(context.Background(), jetstream.Config{URLs: []string{server.URL()}, Name: "test-publisher"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	publisher, err := events.NewPublisher(client, registry)
	require.NoError(t, err)
	return publisher
}

func TestStartDeliversPeriodAndBackfilledEventsToTriggersAndRetriesFailures(t *testing.T) {
	server := startBus(t)
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	triggers := &fakeTriggers{periodErrs: 1}
	consumers, err := Start(context.Background(), ConsumersConfig{URLs: []string{server.URL()}, SpaceIDs: []string{"crypto"}, FetchMaxWait: 100 * time.Millisecond}, registry,
		&PeriodHandler{Registry: registry, Trigger: triggers, NakDelay: 100 * time.Millisecond},
		&BackfilledHandler{Registry: registry, Trigger: triggers, NakDelay: 100 * time.Millisecond})
	require.NoError(t, err)
	t.Cleanup(func() { _ = consumers.Close() })
	publisher := publisherFor(t, server, registry)

	period := time.Date(2026, 10, 21, 10, 10, 0, 0, time.UTC)
	_, err = publisher.Publish(context.Background(), events.DatasetPeriodCompleted, &storagepb.DatasetPeriodCompleted{
		DatasetId: "dataset_src", Frequency: "5m", PeriodTime: period.Unix(), Status: "complete",
		UniverseSubjectIds: []string{"BTC"}, Source: "ledger", SnapshotRef: "snapshot", CompletedAt: timestamppb.New(period.Add(6 * time.Minute)),
	}, events.PublishOptions{EventID: "evt-period", OccurredAt: period.Add(5 * time.Minute), SpaceID: "crypto", SubjectID: "dataset_src"})
	require.NoError(t, err)
	confirmed := period.Add(time.Hour)
	_, err = publisher.Publish(context.Background(), events.CollectorKlineBackfilled, &storagepb.CollectorKlineBackfilled{
		DatasetId: "dataset_src", Frequency: "5m", SubjectIds: []string{"BTC"}, Start: timestamppb.New(period), End: timestamppb.New(period.Add(5 * time.Minute)),
		Origin: "auto", SyncRequestId: "kbf-1", ConfirmedAt: timestamppb.New(confirmed),
	}, events.PublishOptions{EventID: "kbf-1", OccurredAt: confirmed, SpaceID: "crypto", SubjectID: "dataset_src"})
	require.NoError(t, err)

	// 第一次处理周期事件失败后延迟重试，最终两个事件都送到触发器。
	require.Eventually(t, func() bool {
		periods, backfills := triggers.counts()
		return periods == 1 && backfills == 1
	}, 5*time.Second, 50*time.Millisecond)
	triggers.mu.Lock()
	defer triggers.mu.Unlock()
	require.Equal(t, []string{"crypto/dataset_src"}, triggers.periods)
	require.Equal(t, []string{"crypto/kbf-1/dataset_src"}, triggers.backfills)
}
