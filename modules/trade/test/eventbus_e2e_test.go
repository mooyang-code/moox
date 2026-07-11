package test

import (
	"context"
	"encoding/json"
	"github.com/mooyang-code/moox/modules/trade/internal/application/command"
	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/ledger"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	tradebus "github.com/mooyang-code/moox/modules/trade/internal/infra/bus"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/jetstream"
	nserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestJetStreamOutboxToSubmissionInboxE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ns, err := nserver.NewServer(&nserver.Options{JetStream: true, Port: -1, StoreDir: t.TempDir(), NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}
	go ns.Start()
	if !ns.ReadyForConnections(5 * time.Second) {
		t.Fatal("nats not ready")
	}
	defer ns.Shutdown()
	clientURL := strings.Replace(ns.ClientURL(), "0.0.0.0", "127.0.0.1", 1)
	nc, err := nats.Connect(clientURL)
	if err != nil {
		t.Fatal(err)
	}
	js, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = js.AddStream(&nats.StreamConfig{Name: "MOOX_TRADE", Subjects: []string{"moox.trade.>"}, Storage: nats.MemoryStorage}); err != nil {
		t.Fatal(err)
	}
	if _, err = js.AddConsumer("MOOX_TRADE", &nats.ConsumerConfig{Durable: "trade_execution_v1", FilterSubject: "moox.trade.execution.slice_ready.v1", AckPolicy: nats.AckExplicitPolicy, AckWait: time.Minute, MaxDeliver: -1, MaxAckPending: 256, DeliverPolicy: nats.DeliverAllPolicy}); err != nil {
		t.Fatal(err)
	}
	client, err := jetstream.Connect(ctx, jetstream.Config{URLs: []string{clientURL}, Name: "trade-e2e"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	pull, err := client.BindPullConsumer(ctx, jetstream.ConsumerRef{Stream: "MOOX_TRADE", Durable: "trade_execution_v1", FilterSubject: "moox.trade.execution.slice_ready.v1", AckWait: time.Minute, MaxDeliver: -1, MaxAckPending: 256, FetchMaxWait: time.Second, DeliverPolicy: nats.DeliverAllPolicy})
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Transaction(ctx, func(tx *store.Tx) error {
		return tx.PostLedger("space", ledger.Transaction{ID: "seed", BizType: "deposit", RefType: "deposit", RefID: "seed", Entries: []ledger.Entry{{AccountID: "exchange-funding", Asset: "USDT", Bucket: "funding", Amount: shared.MustDecimal("100").Neg()}, {AccountID: "account", Asset: "USDT", Bucket: "available", Amount: shared.MustDecimal("100")}}})
	}); err != nil {
		t.Fatal(err)
	}
	fx := &scriptedExchange{}
	engine := &command.Engine{Store: s, Adapter: fx}
	r, err := engine.Place(ctx, command.PlaceInput{SpaceID: "space", OrderID: "o-event", ClientOrderID: "c-event", AccountID: "account", ChannelID: "channel", Symbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", Side: "BUY", Quantity: "1", Price: "10"})
	if err != nil {
		t.Fatal(err)
	}
	relay := tradebus.Relay{Store: s, Publisher: client, InstanceID: "e2e", BootID: "boot"}
	if err = relay.RunOnce(ctx, 10); err != nil {
		t.Fatal(err)
	}
	ds, err := pull.Fetch(ctx, 1)
	if err != nil || len(ds) != 1 {
		t.Fatalf("fetch %d: %v", len(ds), err)
	}
	d := ds[0]
	var wrapped wrapperspb.BytesValue
	if err = proto.Unmarshal(d.Message.Payload, &wrapped); err != nil {
		t.Fatal(err)
	}
	var eventOrder store.OrderRecord
	if err = json.Unmarshal(wrapped.Value, &eventOrder); err != nil {
		t.Fatal(err)
	}
	if eventOrder.OrderID != r.OrderID {
		t.Fatalf("event order=%s", eventOrder.OrderID)
	}
	worker := consumer.SubmissionWorker{Engine: engine}
	if _, err = worker.Handle(ctx, eventOrder.SpaceID, eventOrder.OrderID); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after the business commit but before Inbox/ACK. The
	// redelivery must not submit the exchange order twice.
	if err = d.Nak(ctx, 0); err != nil {
		t.Fatal(err)
	}
	redelivered, fetchErr := pull.Fetch(ctx, 1)
	if fetchErr != nil || len(redelivered) != 1 {
		t.Fatalf("redelivery %d: %v", len(redelivered), fetchErr)
	}
	d = redelivered[0]
	if _, err = worker.Handle(ctx, eventOrder.SpaceID, eventOrder.OrderID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RecordInbox(ctx, "trade_execution_v1", d.Message.MessageId, d.Message.Topic); err != nil {
		t.Fatal(err)
	}
	if err = d.Ack(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetOrder(ctx, "space", "o-event")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "OPEN" || fx.placeCalls != 1 {
		t.Fatalf("order=%+v calls=%d", got, fx.placeCalls)
	}
	if err = relay.RunOnce(ctx, 10); err != nil {
		t.Fatal(err)
	}
}
