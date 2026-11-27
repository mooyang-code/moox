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
	consumerClient, err := jetstream.Connect(context.Background(), jetstream.Config{URLs: []string{server.URL()}, Name: "test-consumers"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = consumerClient.Close() })
	consumers, err := Start(context.Background(), consumerClient, ConsumersConfig{FetchMaxWait: 100 * time.Millisecond}, registry,
		&PeriodHandler{Registry: registry, Trigger: triggers, NakDelay: 100 * time.Millisecond},
		&BackfilledHandler{Registry: registry, Trigger: triggers, NakDelay: 100 * time.Millisecond})
	require.NoError(t, err)
	t.Cleanup(func() { _ = consumers.Close() })
	publisher := publisherFor(t, server, registry)

	period := time.Date(2026, 10, 21, 10, 10, 0, 0, time.UTC)
	_, err = publisher.Publish(context.Background(), events.DatasetPeriodCompleted, &storagepb.DatasetPeriodCompleted{
		DatasetId: "dataset_src", Frequency: "5m", PeriodTime: period.Unix(), Status: "complete",
		UniverseSubjectIds: []string{"BTC"},
		Source:             "ledger", SnapshotRef: "snapshot", CompletedAt: period.Add(6 * time.Minute).UnixMilli(),
	}, events.PublishOptions{EventID: "evt-period", OccurredAt: period.Add(5 * time.Minute), SpaceID: "crypto", SubjectID: "dataset_src"})
	require.NoError(t, err)
	// 通配消费者同时接收任意自定义空间的事件。
	_, err = publisher.Publish(context.Background(), events.DatasetPeriodCompleted, &storagepb.DatasetPeriodCompleted{
		DatasetId: "dataset_ext", Frequency: "5m", PeriodTime: period.Unix(), Status: "complete",
		UniverseSubjectIds: []string{"SYM"},
		Source:             "ledger", SnapshotRef: "snapshot", CompletedAt: period.Add(6 * time.Minute).UnixMilli(),
	}, events.PublishOptions{EventID: "evt-period-sandbox", OccurredAt: period.Add(5 * time.Minute), SpaceID: "sandbox", SubjectID: "dataset_ext"})
	require.NoError(t, err)
	confirmed := period.Add(time.Hour)
	_, err = publisher.Publish(context.Background(), events.CollectorKlineBackfilled, &storagepb.CollectorKlineBackfilled{
		DatasetId: "dataset_src", Frequency: "5m", SubjectIds: []string{"BTC"}, Start: period.Unix(), End: period.Add(5 * time.Minute).Unix(),
		Origin: "auto", ConfirmedAt: confirmed.UnixMilli(),
	}, events.PublishOptions{EventID: "kbf-1", OccurredAt: confirmed, SpaceID: "sandbox", SubjectID: "dataset_src"})
	require.NoError(t, err)

	// 第一次处理周期事件失败后延迟重试，最终跨空间事件都送到触发器。
	require.Eventually(t, func() bool {
		periods, backfills := triggers.counts()
		return periods == 2 && backfills == 1
	}, 5*time.Second, 50*time.Millisecond)
	triggers.mu.Lock()
	defer triggers.mu.Unlock()
	require.ElementsMatch(t, []string{"crypto/dataset_src", "sandbox/dataset_ext"}, triggers.periods)
	require.Equal(t, []string{"sandbox/kbf-1/dataset_src"}, triggers.backfills)
}

// 任一消费者创建失败则整体失败（已建的被关闭），由 Hub 之后整体重试。
func TestStartFailsAsAWholeWhenAnyConsumerCannotBeCreated(t *testing.T) {
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	triggers := &fakeTriggers{}
	// 只有 MOOX_STORAGE：源周期完成消费者能建，补采完成消费者建不出来。
	server := testkit.Start(t)
	server.AddStream(t, &nats.StreamConfig{Name: "MOOX_STORAGE", Subjects: []string{"moox.event.storage.dataset.period.completed.v1.>"}, Storage: nats.MemoryStorage})
	client, err := jetstream.Connect(context.Background(), jetstream.Config{URLs: []string{server.URL()}, Name: "test-consumers"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	consumers, err := Start(context.Background(), client, ConsumersConfig{FetchMaxWait: 100 * time.Millisecond}, registry,
		&PeriodHandler{Registry: registry, Trigger: triggers}, &BackfilledHandler{Registry: registry, Trigger: triggers})
	require.Error(t, err)
	require.Nil(t, consumers)
}
