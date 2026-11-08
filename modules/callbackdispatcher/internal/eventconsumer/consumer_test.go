package eventconsumer

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/action"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/delivery"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/dispatch"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/store"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/schema"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/jetstream/testkit"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type recordingSender struct {
	requests chan delivery.Request
}

func (r *recordingSender) Send(_ context.Context, _ delivery.Target, request delivery.Request) delivery.Result {
	r.requests <- request
	return delivery.Result{Class: delivery.Success, StatusCode: 200}
}

// startBus 按 EventBus 配置的主题建流：MOOX_COLLECTOR 里有补写和下架两个主题。
func startBus(t *testing.T) *testkit.Server {
	t.Helper()
	server := testkit.Start(t)
	server.AddStream(t, &nats.StreamConfig{Name: "MOOX_STORAGE", Storage: nats.MemoryStorage, Subjects: []string{
		"moox.event.storage.dataset.period.completed.v1.>", "moox.event.storage.view.data.ready.v2.>",
	}})
	server.AddStream(t, &nats.StreamConfig{Name: "MOOX_COLLECTOR", Storage: nats.MemoryStorage, Subjects: []string{
		"moox.event.collector.kline.backfilled.v1.>", "moox.event.collector.instrument.delist_scheduled.v1.>",
	}})
	return server
}

func publishDelist(t *testing.T, server *testkit.Server, symbol string, delistAt time.Time) {
	t.Helper()
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	client, err := jetstream.Connect(context.Background(), jetstream.Config{URLs: []string{server.URL()}, Name: "test-publisher"})
	require.NoError(t, err)
	defer client.Close()
	publisher, err := events.NewPublisher(client, registry)
	require.NoError(t, err)
	seen := time.Now().UTC().Truncate(time.Second)
	_, err = publisher.Publish(context.Background(), events.InstrumentDelistScheduled, &storagepb.InstrumentDelistScheduled{
		Exchange: "binance", Market: events.DelistMarketSpot, ExchangeSymbol: symbol, DelistAt: timestamppb.New(delistAt),
		Source: events.DelistSourceManual, FirstSeenAt: timestamppb.New(seen), Scope: events.DelistScopeAll,
	}, events.PublishOptions{
		EventID: events.InstrumentDelistEventID("binance", events.DelistMarketSpot, symbol, events.DelistScopeAll, delistAt), OccurredAt: seen,
		SpaceID: events.DelistSystemSpaceID, SubjectID: events.InstrumentDelistSubjectID("binance", events.DelistMarketSpot, symbol),
	})
	require.NoError(t, err)
}

// 端到端：EventBus 晚于事件中心启动、事件早于消费者创建，消费者仍能连上并收到（下架事件用 DeliverAll），
// 经匹配生成执行记录，由工作线程通知到规则。
func TestEventBusStartedLateStillDeliversEarlierDelistEvent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := startBus(t)
	delistAt := time.Now().UTC().Add(7 * 24 * time.Hour).Truncate(time.Second)
	// 消费者创建之前就已经发布：靠 DeliverAll 收到。
	publishDelist(t, server, "FOOUSDT", delistAt)

	dbStore, err := store.Open(filepath.Join(t.TempDir(), "dispatcher.db"))
	require.NoError(t, err)
	defer dbStore.Close()
	require.NoError(t, dbStore.ApplySchema(schema.AllSQL()))
	require.NoError(t, dbStore.CreateRule(ctx, store.Rule{
		RuleID: "r1", Name: "币安下架", EventName: events.InstrumentDelistScheduled.Name(),
		Scope:      []store.ScopeFilter{{Field: "exchange", Values: []string{"binance"}}},
		ActionType: store.ActionNotifyURL, NotifyURL: "https://example.com/hook", Secret: "secret", Enabled: true, CreatedAt: 1, UpdatedAt: 1,
	}))
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)

	sender := &recordingSender{requests: make(chan delivery.Request, 4)}
	worker := &dispatch.Worker{Store: dbStore, Sender: sender, Actions: action.NewRegistry(), Batch: 4, PollInterval: 20 * time.Millisecond, RetryWindow: time.Hour, RetentionDays: 1}
	go func() { _ = worker.Run(ctx) }()

	// 前两次取配置都失败（模拟 EventBus 还没起来），之后才成功。
	var configCalls atomic.Int32
	var connected atomic.Bool
	supervisor := &Supervisor{
		Config: func() (jetstream.Config, error) {
			if configCalls.Add(1) <= 2 {
				return jetstream.Config{}, errors.New("EventBus 还没起来")
			}
			return jetstream.Config{URLs: []string{server.URL()}, Name: "moox-callback-dispatcher"}, nil
		},
		Registry: registry, Handler: Handler{Processor: &dispatch.Intake{Store: dbStore, Registry: registry}},
		SetConnected: connected.Store, ReconnectInterval: 50 * time.Millisecond, FetchMaxWait: 100 * time.Millisecond, RetryInterval: 50 * time.Millisecond,
	}
	go func() { _ = supervisor.Run(ctx) }()

	select {
	case request := <-sender.requests:
		require.Equal(t, "r1", request.RuleID)
		require.Equal(t, "binance:SPOT:FOOUSDT", request.SubjectID)
		require.Equal(t, events.InstrumentDelistEventID("binance", events.DelistMarketSpot, "FOOUSDT", events.DelistScopeAll, delistAt), request.EventID)
		require.Contains(t, request.PayloadJSON, `"exchange_symbol":"FOOUSDT"`)
	case <-time.After(10 * time.Second):
		t.Fatal("没有在期限内收到通知")
	}
	require.GreaterOrEqual(t, int(configCalls.Load()), 3)
	require.Eventually(t, connected.Load, time.Second, 10*time.Millisecond)

	// 每个事件一个固定的 durable，名字与凭据里授权的一致。
	for _, event := range events.CallbackDispatcherEvents {
		_, err := server.JetStream().ConsumerInfo(event.Stream(), events.CallbackDispatcherConsumerName(event))
		require.NoError(t, err, event.Name())
	}
	info, err := server.JetStream().ConsumerInfo("MOOX_COLLECTOR", events.CallbackDispatcherConsumerName(events.InstrumentDelistScheduled))
	require.NoError(t, err)
	require.Equal(t, nats.DeliverAllPolicy, info.Config.DeliverPolicy)
	require.Equal(t, -1, info.Config.MaxDeliver, "写库失败不能丢事件，不限次数")
	other, err := server.JetStream().ConsumerInfo("MOOX_STORAGE", events.CallbackDispatcherConsumerName(events.ViewDataReady))
	require.NoError(t, err)
	require.Equal(t, nats.DeliverNewPolicy, other.Config.DeliverPolicy, "其余事件第一次上线不回放历史")

	// 重复发布同一条下架（同一事件 ID）：回执挡住，不会再次通知。
	publishDelist(t, server, "FOOUSDT", delistAt)
	select {
	case <-sender.requests:
		t.Fatal("同一事件不应该再次通知")
	case <-time.After(500 * time.Millisecond):
	}
	// 另一个标的是新的事件。
	publishDelist(t, server, "BARUSDT", delistAt)
	select {
	case request := <-sender.requests:
		require.Equal(t, "binance:SPOT:BARUSDT", request.SubjectID)
	case <-time.After(10 * time.Second):
		t.Fatal("没有收到第二个标的的通知")
	}
}
