package eventbus

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/jetstream/testkit"
	"github.com/mooyang-code/moox/packages/storagepb"
	nats "github.com/nats-io/nats.go"
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

func TestFailedConnectedCallbackDiscardsTheConnectionAndRetries(t *testing.T) {
	server := testkit.Start(t)
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	hub := NewHub(Config{URLs: []string{server.URL()}}, registry)
	hub.RetryInterval = 10 * time.Millisecond
	t.Cleanup(func() { _ = hub.Close() })
	attempts := 0
	hub.OnConnected(func(context.Context, *jetstream.Client) (func() error, error) {
		attempts++
		if attempts == 1 {
			return nil, errors.New("消费者创建失败")
		}
		return nil, nil
	})
	require.Error(t, hub.Ensure(context.Background()))
	require.Nil(t, hub.Client(), "回调失败的连接作废")
	require.Eventually(t, func() bool { return hub.Ensure(context.Background()) == nil }, 2*time.Second, 10*time.Millisecond)
	require.NotNil(t, hub.Client())
	require.NotNil(t, hub.Publisher())
	require.Equal(t, 2, attempts)
}
