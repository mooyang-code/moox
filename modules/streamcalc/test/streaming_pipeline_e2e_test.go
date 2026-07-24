package test

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/streamcalc/internal/aggregate"
	"github.com/mooyang-code/moox/modules/streamcalc/internal/service"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/marketpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type fakeWriter struct{ bars []aggregate.Bar }

func (w *fakeWriter) Write(_ context.Context, bar aggregate.Bar) error {
	w.bars = append(w.bars, bar)
	return nil
}

func TestCollectorEventToStreamcalcAggregationE2E(t *testing.T) {
	server := startJetStream(t)
	defer server.Shutdown()
	nc, err := nats.Connect(server.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		t.Fatal(err)
	}
	if _, err := js.AddStream(&nats.StreamConfig{Name: "MOOX_MARKET", Subjects: []string{"moox.market.>"}, Storage: nats.MemoryStorage, MaxAge: time.Hour}); err != nil {
		nc.Close()
		t.Fatal(err)
	}
	if _, err := js.AddConsumer("MOOX_MARKET", &nats.ConsumerConfig{Name: "streamcalc_kline_v1", Durable: "streamcalc_kline_v1", FilterSubject: "moox.market.>", AckPolicy: nats.AckExplicitPolicy, AckWait: time.Second, MaxAckPending: 32, MaxDeliver: 3, DeliverPolicy: nats.DeliverAllPolicy}); err != nil {
		nc.Close()
		t.Fatal(err)
	}
	nc.Close()

	client, err := jetstream.Connect(context.Background(), jetstream.Config{URLs: []string{server.ClientURL()}, Name: "streamcalc-e2e"})
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
	consumer, err := events.NewConsumer(client, jetstream.ConsumerBindRef{Stream: "MOOX_MARKET", Durable: "streamcalc_kline_v1", FetchMaxWait: 50 * time.Millisecond}, registry)
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	aggregator, err := aggregate.New("1m", "5m", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	writer := new(fakeWriter)
	processor, err := service.NewProcessor(aggregator, writer)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := service.NewRunner(consumer, processor, 8)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		start := base.Add(time.Duration(i) * time.Minute)
		payload := &marketpb.Tick{Exchange: "binance", TradeId: fmt.Sprintf("trade-%d", i), Symbol: "BTC-USDT", Price: 100, Quantity: 1, TradeTime: timestamppb.New(start.Add(10 * time.Second))}
		if _, err := publisher.Publish(context.Background(), events.TickReceived, payload, events.PublishOptions{EventID: fmt.Sprintf("binance:%s:%d", payload.GetSymbol(), i), OccurredAt: start.Add(time.Minute), SpaceID: "crypto", SubjectID: payload.GetSymbol()}); err != nil {
			t.Fatal(err)
		}
	}
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(writer.bars) != 1 || !writer.bars[0].Closed || writer.bars[0].Volume != 5 || writer.bars[0].TradeCount != 5 {
		t.Fatalf("aggregated bars = %+v", writer.bars)
	}
	duplicate := &marketpb.Tick{Exchange: "binance", TradeId: "trade-4", Symbol: "BTC-USDT", Price: 100, Quantity: 1, TradeTime: timestamppb.New(base.Add(4*time.Minute + 10*time.Second))}
	if _, err := publisher.Publish(context.Background(), events.TickReceived, duplicate, events.PublishOptions{EventID: "binance:BTC-USDT:4", OccurredAt: base.Add(5 * time.Minute), SpaceID: "crypto", SubjectID: duplicate.GetSymbol()}); err != nil {
		t.Fatal(err)
	}
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(writer.bars) != 1 {
		t.Fatalf("duplicate produced another storage write: %+v", writer.bars)
	}
}

func startJetStream(t *testing.T) *natsserver.Server {
	t.Helper()
	port := freePort(t)
	server, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: port, JetStream: true, StoreDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	server.Start()
	if !server.ReadyForConnections(5 * time.Second) {
		server.Shutdown()
		t.Fatal("embedded NATS did not become ready")
	}
	return server
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}
