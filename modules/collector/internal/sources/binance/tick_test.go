package binance

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/model/common"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/exchange"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/marketpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"
)

type fakeRecentTradeAPI struct {
	requests []*exchange.TradeRequest
	trades   []*exchange.Trade
}

func (f *fakeRecentTradeAPI) GetRecentTrades(_ context.Context, req *exchange.TradeRequest) ([]*exchange.Trade, error) {
	copyReq := *req
	f.requests = append(f.requests, &copyReq)
	return f.trades, nil
}

func TestTickCollectorPublishesOrderedDeduplicatedTicks(t *testing.T) {
	now := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	api := &fakeRecentTradeAPI{trades: []*exchange.Trade{
		{ID: 3, Price: common.NewDecimal("103"), Quantity: common.NewDecimal("0.3"), TradeTime: now.Add(3 * time.Second)},
		{ID: 2, Price: common.NewDecimal("102"), Quantity: common.NewDecimal("0.2"), TradeTime: now.Add(2 * time.Second)},
		{ID: 2, Price: common.NewDecimal("102"), Quantity: common.NewDecimal("0.2"), TradeTime: now.Add(2 * time.Second)},
		{ID: 1, Price: common.NewDecimal("101"), Quantity: common.NewDecimal("0.1"), TradeTime: now.Add(time.Second)},
	}}
	type published struct {
		event events.EventType
		msg   proto.Message
		opts  events.PublishOptions
	}
	var got []published
	c := &TickCollector{spotAPI: api, lastIDs: make(map[string]int64), publish: func(_ context.Context, event events.EventType, msg proto.Message, opts events.PublishOptions) error {
		got = append(got, published{event: event, msg: msg, opts: opts})
		return nil
	}}
	params := &sources.CollectParams{SpaceID: "crypto", InstType: InstTypeSPOT, Symbol: "BTCUSDT", SubjectID: "BTC-USDT"}
	if err := c.Collect(context.Background(), params); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("published %d ticks, want 3", len(got))
	}
	for i, item := range got {
		payload, ok := item.msg.(*marketpb.Tick)
		if !ok {
			t.Fatalf("published payload %T, want Tick", item.msg)
		}
		wantID := int64(i + 1)
		if payload.GetTradeId() != formatTradeID(wantID) || item.event != events.TickReceived || item.opts.SubjectID != "BTC-USDT" || item.opts.SpaceID != "crypto" {
			t.Fatalf("tick %d = payload=%v opts=%+v event=%+v", i, payload, item.opts, item.event)
		}
	}
	if len(api.requests) != 1 || api.requests[0].FromID != 0 {
		t.Fatalf("first request = %+v, want from_id=0", api.requests)
	}
	if err := c.Collect(context.Background(), params); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || len(api.requests) != 2 || api.requests[1].FromID != 4 {
		t.Fatalf("second poll did not use cursor: published=%d requests=%+v", len(got), api.requests)
	}
}

func TestTickCollectorPublishesThroughRealEventPublisherE2E(t *testing.T) {
	server, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	go server.Start()
	if !server.ReadyForConnections(10 * time.Second) {
		t.Fatal("embedded NATS did not start")
	}
	defer server.Shutdown()
	nc, err := nats.Connect(server.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := js.AddStream(&nats.StreamConfig{Name: "MOOX_MARKET", Subjects: []string{"moox.market.>"}, Storage: nats.MemoryStorage}); err != nil {
		t.Fatal(err)
	}
	if _, err := js.AddConsumer("MOOX_MARKET", &nats.ConsumerConfig{Name: "collector-e2e", Durable: "collector-e2e", FilterSubject: "moox.market.tick.received.v1.>", AckPolicy: nats.AckExplicitPolicy, AckWait: time.Second}); err != nil {
		t.Fatal(err)
	}
	client, err := jetstream.Connect(context.Background(), jetstream.ConfigFromEnv([]string{server.ClientURL()}, "collector-e2e"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := events.NewPublisher(client, registry)
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := events.NewConsumer(client, jetstream.ConsumerBindRef{Stream: "MOOX_MARKET", Durable: "collector-e2e", FetchMaxWait: 100 * time.Millisecond}, registry)
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	previousPublisher := eventPublisher
	eventPublisher = publisher
	defer func() { eventPublisher = previousPublisher }()
	api := &fakeRecentTradeAPI{trades: []*exchange.Trade{{ID: 7, Price: common.NewDecimal("101.5"), Quantity: common.NewDecimal("0.25"), TradeTime: time.Date(2026, 7, 24, 8, 0, 0, 0, time.UTC)}}}
	collector := &TickCollector{spotAPI: api, lastIDs: make(map[string]int64)}
	if err := collector.Collect(context.Background(), &sources.CollectParams{SpaceID: "crypto_binance", InstType: InstTypeSPOT, Symbol: "BTCUSDT", SubjectID: "BTC-USDT", Live: true}); err != nil {
		t.Fatal(err)
	}
	deliveries, err := consumer.Fetch(context.Background(), 1)
	if err != nil || len(deliveries) != 1 {
		t.Fatalf("collector event deliveries=%d err=%v", len(deliveries), err)
	}
	if tick, ok := deliveries[0].Payload.(*marketpb.Tick); !ok || tick.GetTradeId() != "7" || deliveries[0].Message.GetEventName() != events.TickReceived.Name() {
		t.Fatalf("collector delivery=%#v", deliveries[0])
	}
	if err := deliveries[0].Delivery.Ack(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func formatTradeID(id int64) string { return fmt.Sprintf("%d", id) }
