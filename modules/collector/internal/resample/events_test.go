package resample

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/jetstream/testkit"
	"github.com/mooyang-code/moox/packages/storagepb"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type recordingRelay struct {
	mu       sync.Mutex
	messages []*eventpb.EventMessage
	failures int
}

func (r *recordingRelay) PublishMessage(_ context.Context, message *eventpb.EventMessage) (*jetstream.PublishAck, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failures > 0 {
		r.failures--
		return nil, context.DeadlineExceeded
	}
	r.messages = append(r.messages, message)
	return &jetstream.PublishAck{}, nil
}

type recordingSync struct {
	mu    sync.Mutex
	calls [][2]string
}

func (r *recordingSync) AppendDatasetSyncPoint(_ context.Context, _, datasetID, requestID, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, [2]string{datasetID, requestID})
	return nil
}

func TestNoticePublisherMergesNoticesIntoOneEventPerGroupWithSyncPoint(t *testing.T) {
	mgr := openState(t)
	ctx := context.Background()
	start := utc("2026-10-21T10:00:00Z")
	notice := func(dataset, subject, origin, job string, from, to time.Time) store.BackfillNotice {
		return store.BackfillNotice{SpaceID: "crypto", DatasetID: dataset, Frequency: "5m", SubjectID: subject, Start: from, End: to, Origin: origin, JobID: job}
	}
	require.NoError(t, mgr.Resample().AddNotices(ctx, []store.BackfillNotice{
		notice("dataset_a", "BTC", "auto", "job-1", start, start.Add(time.Hour)),
		notice("dataset_a", "ETH", "auto", "job-1", start.Add(30*time.Minute), start.Add(2*time.Hour)),
		notice("dataset_a", "BTC", "manual", "job-2", start, start.Add(time.Hour)),
		notice("dataset_b", "BTC", "resample", "job-3", start, start.Add(time.Hour)),
	}))
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	relay := &recordingRelay{}
	sync := &recordingSync{}
	publisher := &NoticePublisher{State: mgr.Resample(), Sync: sync, Registry: registry, Relay: relay, Now: func() time.Time { return start.Add(3 * time.Hour) }}

	relay.failures = 1 // 第一次发布失败：事件留在发件箱，下一次重发
	require.Error(t, publisher.Flush(ctx))
	require.Empty(t, relay.messages)
	require.NoError(t, publisher.Flush(ctx))
	require.Len(t, relay.messages, 3, "三个分组各一条事件：同数据集不同来源/作业不合并")
	require.Len(t, sync.calls, 3, "每个分组先追加一个同步点")

	byJob := map[string]*storagepb.CollectorKlineBackfilled{}
	for _, message := range relay.messages {
		value := decodePayload(t, registry, message)
		byJob[value.GetJobId()] = value
		require.Equal(t, value.GetDatasetId(), message.GetSubjectId())
		require.Equal(t, value.GetSyncRequestId(), message.GetEventId(), "同步点 request_id 就是事件 ID")
	}
	merged := byJob["job-1"]
	require.Equal(t, []string{"BTC", "ETH"}, merged.GetSubjectIds())
	require.Equal(t, start, merged.GetStart().AsTime())
	require.Equal(t, start.Add(2*time.Hour), merged.GetEnd().AsTime(), "区间取并集")
	require.Equal(t, "auto", merged.GetOrigin())
	require.Equal(t, "resample", byJob["job-3"].GetOrigin())

	again, err := mgr.Resample().PendingEvents(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, again, "发布成功后发件箱清空")
	left, _ := mgr.Resample().UnsealedNotices(ctx, 10)
	require.Empty(t, left)
}

func decodePayload(t *testing.T, registry *events.Registry, message *eventpb.EventMessage) *storagepb.CollectorKlineBackfilled {
	t.Helper()
	event, err := registry.ValidateMessage(message)
	require.NoError(t, err)
	payload := event.NewPayload()
	require.NoError(t, proto.Unmarshal(message.GetPayload(), payload))
	return payload.(*storagepb.CollectorKlineBackfilled)
}

func TestNoticePublisherBatchIDIsDeterministicSoReplayIsIdempotent(t *testing.T) {
	require.Equal(t, batchID([]int64{3, 1, 2}), batchID([]int64{1, 2, 3}))
	require.NotEqual(t, batchID([]int64{1, 2}), batchID([]int64{1, 2, 3}))
}

func TestNoticePublisherWithoutRelayKeepsNoticesForRetention(t *testing.T) {
	mgr := openState(t)
	ctx := context.Background()
	require.NoError(t, mgr.Resample().AddNotices(ctx, []store.BackfillNotice{{SpaceID: "crypto", DatasetID: "d", Frequency: "5m", SubjectID: "A", Start: utc("2026-10-21T10:00:00Z"), End: utc("2026-10-21T11:00:00Z"), Origin: "auto"}}))
	publisher := &NoticePublisher{State: mgr.Resample()}
	require.NoError(t, publisher.Flush(ctx))
	left, err := mgr.Resample().UnsealedNotices(ctx, 10)
	require.NoError(t, err)
	require.Len(t, left, 1)
}

func startBus(t *testing.T) *testkit.Server {
	t.Helper()
	server := testkit.Start(t)
	server.AddStream(t, &nats.StreamConfig{Name: "MOOX_STORAGE", Subjects: []string{"moox.event.storage.collector.period.completed.v1.>"}, Storage: nats.MemoryStorage})
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

func TestConsumersResampleBucketOnLastSourcePeriodEvent(t *testing.T) {
	server := startBus(t)
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	mgr := openState(t)
	storage := newFakeStorage()
	seedTask(t, mgr, storage, []string{"BTC", "ETH", "SOL"}, utc("2026-10-21T10:00:00Z"), utc("2026-10-21T10:30:00Z"))
	engine := &Engine{Primary: storage, State: mgr.Resample()}
	index := &TaskIndex{Tasks: mgr.Tasks()}
	consumers, err := StartConsumers(context.Background(), ConsumersConfig{URLs: []string{server.URL()}, SpaceIDs: []string{"crypto"}, FetchMaxWait: 100 * time.Millisecond}, registry,
		&PeriodHandler{Registry: registry, Index: index, Engine: engine, NakDelay: time.Second},
		&BackfilledHandler{Registry: registry, Index: index, State: mgr.Resample(), SettleAfter: time.Minute})
	require.NoError(t, err)
	t.Cleanup(func() { _ = consumers.Close() })
	publisher := publisherFor(t, server, registry)

	publish := func(period time.Time, status string, failed ...string) {
		payload := &storagepb.CollectorPeriodCompleted{
			DatasetId: "dataset_src", Frequency: "5m", PeriodTime: period.Unix(), Status: status,
			UniverseSubjectIds: []string{"BTC", "ETH", "SOL"}, FailedSubjects: failed, CollectedAt: timestamppb.New(period.Add(6 * time.Minute)),
			BatchId: "batch-" + period.Format("1504"), ConfigSnapshotId: "snapshot", ExpectedScopeRef: "scope",
		}
		_, err := publisher.Publish(context.Background(), events.CollectorPeriodCompleted, payload, events.PublishOptions{
			EventID: "evt-" + period.Format("1504") + status, OccurredAt: period.Add(5 * time.Minute), SpaceID: "crypto", SubjectID: "dataset_src",
		})
		require.NoError(t, err)
	}
	// 10:00 桶（10:00–10:15）的前两根源 K 线事件不触发；最后一根（10:10）触发。SOL 在最后一根失败。
	publish(utc("2026-10-21T10:00:00Z"), "complete")
	publish(utc("2026-10-21T10:05:00Z"), "complete")
	time.Sleep(300 * time.Millisecond)
	require.Nil(t, storage.target("crypto", "dataset_task", "BTC", "15m", "venue:binance", utc("2026-10-21T10:00:00Z")), "不是桶内最后一根源周期，不触发")
	publish(utc("2026-10-21T10:10:00Z"), "degraded", "SOL")

	require.Eventually(t, func() bool {
		storage.mu.Lock()
		defer storage.mu.Unlock()
		return storage.rows[keyText(rowKey("crypto", "dataset_task", "BTC", "15m", utc("2026-10-21T10:00:00Z"), "venue:binance"))] != nil &&
			storage.rows[keyText(rowKey("crypto", "dataset_task", "ETH", "15m", utc("2026-10-21T10:00:00Z"), "venue:binance"))] != nil
	}, 5*time.Second, 50*time.Millisecond)
	storage.mu.Lock()
	solRow := storage.rows[keyText(rowKey("crypto", "dataset_task", "SOL", "15m", utc("2026-10-21T10:00:00Z"), "venue:binance"))]
	storage.mu.Unlock()
	require.Nil(t, solRow, "degraded 事件里失败的标的先跳过，留给兜底")
}

func TestConsumersSubmitSourceBackfillJobWithSettleDelay(t *testing.T) {
	server := startBus(t)
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	mgr := openState(t)
	storage := newFakeStorage()
	seedTask(t, mgr, storage, []string{"BTC"}, utc("2026-10-21T10:00:00Z"), utc("2026-10-21T10:30:00Z"))
	index := &TaskIndex{Tasks: mgr.Tasks()}
	consumers, err := StartConsumers(context.Background(), ConsumersConfig{URLs: []string{server.URL()}, SpaceIDs: []string{"crypto"}, FetchMaxWait: 100 * time.Millisecond}, registry,
		&PeriodHandler{Registry: registry, Index: index, Engine: &Engine{Primary: storage}},
		&BackfilledHandler{Registry: registry, Index: index, State: mgr.Resample(), SettleAfter: time.Minute, Now: func() time.Time { return utc("2026-10-21T12:00:00Z") }})
	require.NoError(t, err)
	t.Cleanup(func() { _ = consumers.Close() })
	publisher := publisherFor(t, server, registry)

	confirmed := utc("2026-10-21T11:00:00Z")
	payload := &storagepb.CollectorKlineBackfilled{
		DatasetId: "dataset_src", Frequency: "5m", SubjectIds: []string{"BTC"}, Start: timestamppb.New(utc("2026-10-21T10:03:00Z")), End: timestamppb.New(utc("2026-10-21T10:20:00Z")),
		Origin: "auto", SyncRequestId: "kbf-1", ConfirmedAt: timestamppb.New(confirmed),
	}
	for _, id := range []string{"kbf-1", "kbf-1"} { // 重复投递（Msg-Id 去重或重放）不重复建作业
		_, err = publisher.Publish(context.Background(), events.CollectorKlineBackfilled, payload, events.PublishOptions{EventID: id, OccurredAt: confirmed, SpaceID: "crypto", SubjectID: "dataset_src"})
		require.NoError(t, err)
	}
	require.Eventually(t, func() bool {
		jobs, err := mgr.Resample().ListJobs(context.Background(), "crypto", "task", "", 10)
		return err == nil && len(jobs) == 1
	}, 5*time.Second, 50*time.Millisecond)
	jobs, _ := mgr.Resample().ListJobs(context.Background(), "crypto", "task", "", 10)
	job := jobs[0]
	require.Equal(t, store.ResampleOriginSourceBackfill, job.Origin)
	require.Equal(t, []string{"BTC"}, job.Subjects)
	require.Equal(t, utc("2026-10-21T10:00:00Z"), job.Start, "向外对齐到目标桶")
	require.Equal(t, utc("2026-10-21T10:30:00Z"), job.End)
	require.Equal(t, confirmed.Add(time.Minute), job.NotBefore, "最早开始时间 = 确认时刻 + 2×执行超时")
}
