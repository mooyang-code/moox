package eventbus

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/attention"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/jetstream/testkit"
	"github.com/mooyang-code/moox/packages/storagepb"
	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func queueDelist(t *testing.T, db *store.Store, registry *events.Registry, symbol string) {
	t.Helper()
	delistAt := time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)
	eventID := events.InstrumentDelistEventID("binance", events.DelistMarketSpot, symbol, events.DelistScopeAll, delistAt)
	subject := events.InstrumentDelistSubjectID("binance", events.DelistMarketSpot, symbol)
	raw, err := registry.MarshalMessage(events.InstrumentDelistScheduled, &storagepb.InstrumentDelistScheduled{
		Exchange: "binance", Market: events.DelistMarketSpot, ExchangeSymbol: symbol, DelistAt: timestamppb.New(delistAt),
		Source: events.DelistSourceManual, FirstSeenAt: timestamppb.New(delistAt.Add(-time.Hour)), Scope: events.DelistScopeAll,
	}, events.PublishOptions{EventID: eventID, OccurredAt: delistAt.Add(-time.Hour), SpaceID: events.DelistSystemSpaceID, SubjectID: subject})
	require.NoError(t, err)
	created, _, err := db.DelistNotices().Register(context.Background(), store.DelistNotice{
		Exchange: "binance", Market: events.DelistMarketSpot, Symbol: symbol, Scope: events.DelistScopeAll, DelistAt: delistAt,
		Source: events.DelistSourceManual, SeenAt: delistAt.Add(-time.Hour),
	}, func(int) (store.OutboxEvent, error) {
		return store.OutboxEvent{EventID: eventID, SpaceID: events.DelistSystemSpaceID, EventName: events.InstrumentDelistScheduled.Name(), SubjectToken: subject, Payload: raw}, nil
	})
	require.NoError(t, err)
	require.True(t, created)
}

func TestRelayPublishesWithoutKlineResampleAndSurvivesLateEventBus(t *testing.T) {
	server := testkit.Start(t)
	server.AddStream(t, &nats.StreamConfig{Name: "MOOX_COLLECTOR", Storage: nats.MemoryStorage, Subjects: []string{
		"moox.event.collector.kline.backfilled.v1.>", "moox.event.collector.instrument.delist_scheduled.v1.>",
	}})
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	db := openStore(t)
	queueDelist(t, db, registry, "FOOUSDT")

	// EventBus 还没起来：先指向一个没有服务的地址。
	hub := NewHub(Config{URLs: []string{"nats://127.0.0.1:1"}}, registry)
	hub.RetryInterval = 50 * time.Millisecond
	var connectedCalls int
	var mu sync.Mutex
	closed := 0
	hub.OnConnected(func(_ context.Context, client *jetstream.Client) (func() error, error) {
		mu.Lock()
		defer mu.Unlock()
		connectedCalls++
		require.NotNil(t, client)
		return func() error {
			mu.Lock()
			defer mu.Unlock()
			require.True(t, client.Ready(), "依赖连接的消费者必须先于连接关闭")
			closed++
			return nil
		}, nil
	})
	t.Cleanup(func() { _ = hub.Close() })
	relay := NewRelay(db.Resample(), registry, hub, 20*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = relay.Run(ctx) }()

	time.Sleep(300 * time.Millisecond)
	pending, err := db.Resample().PendingEvents(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1, "EventBus 连不上时事件留在发件箱，不丢")
	require.Nil(t, hub.Client())

	// EventBus 起来了（配置指向可用的地址）：自动连上并发布，不需要重启。
	hub.mu.Lock()
	hub.cfg = Config{URLs: []string{server.URL()}}
	hub.mu.Unlock()
	require.Eventually(t, func() bool {
		left, err := db.Resample().PendingEvents(ctx, 10)
		return err == nil && len(left) == 0
	}, 5*time.Second, 20*time.Millisecond)
	info, err := server.JetStream().StreamInfo("MOOX_COLLECTOR")
	require.NoError(t, err)
	require.EqualValues(t, 1, info.State.Msgs)
	mu.Lock()
	require.Equal(t, 1, connectedCalls)
	mu.Unlock()
	require.NoError(t, hub.Close())
	mu.Lock()
	require.Equal(t, 1, closed, "关闭 Hub 时先调用回调返回的关闭函数")
	mu.Unlock()

	// 之后新写入的事件被唤醒后立即发布。
	queueDelist(t, db, registry, "BARUSDT")
	relay.Wake()
	require.Eventually(t, func() bool {
		info, err := server.JetStream().StreamInfo("MOOX_COLLECTOR")
		return err == nil && info.State.Msgs == 2
	}, 5*time.Second, 20*time.Millisecond)
}

// K 线合成的消费者启动失败不能关掉整条连接、挡住下架事件的发布：连接和发布器照样保留，失败的回调之后单独重试。
func TestFailedConnectedCallbackKeepsConnectionAndPublishingAndIsRetriedAlone(t *testing.T) {
	server := testkit.Start(t)
	server.AddStream(t, &nats.StreamConfig{Name: "MOOX_COLLECTOR", Storage: nats.MemoryStorage, Subjects: []string{
		"moox.event.collector.kline.backfilled.v1.>", "moox.event.collector.instrument.delist_scheduled.v1.>",
	}})
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	db := openStore(t)
	hub := NewHub(Config{URLs: []string{server.URL()}}, registry)
	hub.RetryInterval = 50 * time.Millisecond
	t.Cleanup(func() { _ = hub.Close() })

	failing, healthy, closedHealthy := 0, 0, 0
	hub.OnConnected(func(context.Context, *jetstream.Client) (func() error, error) {
		failing++
		if failing <= 2 {
			return nil, errors.New("消费者创建失败")
		}
		return nil, nil
	})
	hub.OnConnected(func(context.Context, *jetstream.Client) (func() error, error) {
		healthy++
		return func() error { closedHealthy++; return nil }, nil
	})

	require.NoError(t, hub.Ensure(context.Background()), "回调失败不影响连接")
	require.NotNil(t, hub.Client())
	require.NotNil(t, hub.Publisher())
	require.Equal(t, 1, failing)
	require.Equal(t, 1, healthy)

	// 回调还在失败期间，下架事件照常发布。
	relay := NewRelay(db.Resample(), registry, hub, time.Second)
	queueDelist(t, db, registry, "FOOUSDT")
	require.NoError(t, relay.Flush(context.Background()))
	info, err := server.JetStream().StreamInfo("MOOX_COLLECTOR")
	require.NoError(t, err)
	require.EqualValues(t, 1, info.State.Msgs)

	// 失败的回调在重试间隔之后单独重试；已成功的回调不会再跑。
	require.NoError(t, hub.Ensure(context.Background()))
	require.Equal(t, 1, failing, "没到重试间隔")
	require.Eventually(t, func() bool {
		require.NoError(t, hub.Ensure(context.Background()))
		return failing == 3
	}, 3*time.Second, 20*time.Millisecond)
	require.Equal(t, 1, healthy)
	require.NoError(t, hub.Close())
	require.Equal(t, 1, closedHealthy)
}

// 无效事件不堵住后面的：标成死信并上报关注项，后面的事件照常发布。
func TestInvalidOutboxEventIsParkedAndDoesNotBlockTheRest(t *testing.T) {
	server := testkit.Start(t)
	server.AddStream(t, &nats.StreamConfig{Name: "MOOX_COLLECTOR", Storage: nats.MemoryStorage, Subjects: []string{
		"moox.event.collector.kline.backfilled.v1.>", "moox.event.collector.instrument.delist_scheduled.v1.>",
	}})
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	db := openStore(t)
	ctx := context.Background()
	// 先放一条无法解码的事件，再放一条正常的。
	_, _, err = db.DelistNotices().Register(ctx, store.DelistNotice{Exchange: "binance", Market: events.DelistMarketSpot, Symbol: "BROKEN", Scope: events.DelistScopeAll, DelistAt: time.Now(), Source: events.DelistSourceManual, SeenAt: time.Now()},
		func(int) (store.OutboxEvent, error) {
			return store.OutboxEvent{EventID: "broken", SpaceID: events.DelistSystemSpaceID, EventName: events.InstrumentDelistScheduled.Name(), SubjectToken: "x", Payload: []byte("not protobuf")}, nil
		})
	require.NoError(t, err)
	queueDelist(t, db, registry, "GOODUSDT")

	attn := attention.New(prometheus.NewRegistry())
	hub := NewHub(Config{URLs: []string{server.URL()}}, registry)
	t.Cleanup(func() { _ = hub.Close() })
	relay := NewRelay(db.Resample(), registry, hub, time.Second)
	relay.Attention = attn
	require.NoError(t, relay.Flush(ctx))

	info, err := server.JetStream().StreamInfo("MOOX_COLLECTOR")
	require.NoError(t, err)
	require.EqualValues(t, 1, info.State.Msgs, "正常的事件照常发布")
	pending, err := db.Resample().PendingEvents(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, pending, "无效事件不再是待发布")
	dead, err := db.Resample().DeadEvents(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, dead)
	require.Equal(t, 1.0, testutil.ToFloat64(attn.Gauge().WithLabelValues(attention.KindOutboxStuck, "事件发件箱有 1 条无效事件被搁置，需要人工查看")))
}

// 发件箱里最老的事件超过 10 分钟没发布出去：上报关注项；发布出去之后恢复。
func TestRelayReportsStuckOutboxAndRecovers(t *testing.T) {
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	db := openStore(t)
	queueDelist(t, db, registry, "FOOUSDT")
	hub := NewHub(Config{URLs: []string{"nats://127.0.0.1:1"}}, registry)
	hub.RetryInterval = time.Hour
	t.Cleanup(func() { _ = hub.Close() })
	attn := attention.New(prometheus.NewRegistry())
	now := time.Now().UTC()
	relay := NewRelay(db.Resample(), registry, hub, time.Second)
	relay.Attention = attn
	relay.Now = func() time.Time { return now }

	require.Error(t, relay.Flush(context.Background()))
	require.Equal(t, 0.0, testutil.ToFloat64(attn.Gauge().WithLabelValues(attention.KindOutboxStuck, "")), "刚写入的事件不算积压")

	now = now.Add(StuckAfter + time.Minute)
	require.Error(t, relay.Flush(context.Background()))
	require.Equal(t, 1.0, testutil.ToFloat64(attn.Gauge().WithLabelValues(attention.KindOutboxStuck, "事件发件箱有事件 11 分钟没发布出去（EventBus 连不上或发布被拒）")))
}
