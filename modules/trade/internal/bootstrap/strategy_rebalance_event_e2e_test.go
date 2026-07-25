package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/command"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/instrument"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/ledger"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/nats-io/nats.go"
)

type strategyTradeE2EAdapter struct{}

func (strategyTradeE2EAdapter) Place(_ context.Context, request exchange.PlaceRequest) (exchange.ExchangeOrderResult, error) {
	return exchange.ExchangeOrderResult{ExchangeOrderID: "exchange-" + request.ClientOrderID, Status: "OPEN"}, nil
}
func (strategyTradeE2EAdapter) Cancel(context.Context, string, string) (exchange.ExchangeOrderResult, error) {
	return exchange.ExchangeOrderResult{Status: "CANCELED"}, nil
}
func (strategyTradeE2EAdapter) QueryByClientOrderID(context.Context, string, string) (exchange.ExchangeOrderResult, error) {
	return exchange.ExchangeOrderResult{Status: "OPEN"}, nil
}
func (strategyTradeE2EAdapter) Rules(context.Context, string) (instrument.Rules, error) {
	return instrument.Rules{
		BaseAsset: "BTC", QuoteAsset: "USDT", StepSize: shared.MustDecimal("0.001"),
	}, nil
}
func (strategyTradeE2EAdapter) ListFills(context.Context, string, string) ([]exchange.FillEvent, error) {
	return nil, nil
}
func (strategyTradeE2EAdapter) SubscribePrivate(context.Context, exchange.PrivateEventHandler) error {
	return nil
}

func TestExternalStrategyRebalanceEventCreatesOneRunAndWakesWorker(t *testing.T) {
	natsURL := os.Getenv("MOOX_STRATEGY_TRADE_E2E_NATS_URL")
	if natsURL == "" {
		t.Skip("set MOOX_STRATEGY_TRADE_E2E_NATS_URL to run the cross-module E2E")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Transaction(ctx, func(tx *store.Tx) error {
		return tx.PostLedger("space", ledger.Transaction{
			ID: shared.LedgerTransactionID("seed-e2e"), BizType: "seed", RefType: "test", RefID: "e2e",
			Entries: []ledger.Entry{
				{AccountID: "clearing", Asset: "USDT", Bucket: "clearing", Amount: shared.MustDecimal("-100")},
				{AccountID: "acct", Asset: "USDT", Bucket: "available", Amount: shared.MustDecimal("100")},
			},
		})
	}); err != nil {
		t.Fatal(err)
	}
	engine := &command.Engine{Store: s, Adapter: strategyTradeE2EAdapter{}}
	if _, err = engine.Place(ctx, command.PlaceInput{
		SpaceID: "space", OrderID: "price-source", ClientOrderID: "price-source",
		AccountID: "acct", ChannelID: "chan", Symbol: "BTC-USDT", MarketType: "spot",
		BaseAsset: "BTC", QuoteAsset: "USDT", Side: "BUY", Quantity: "1", Price: "10",
	}); err != nil {
		t.Fatal(err)
	}

	wakeup := newKernelWakeup()
	s.SetWakeup(wakeup.Wake)
	workerCtx, stopWorker := context.WithCancel(ctx)
	defer stopWorker()
	go runTradeStateWorker(workerCtx, s, engine, wakeup)

	client, err := jetstream.Connect(ctx, jetstream.Config{URLs: []string{natsURL}, Name: "strategy-trade-e2e-consumer"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := events.NewConsumer(ctx, client, registry, events.ConsumerConfig{
		Name: "trade_rebalance_v1", Event: events.TradeRebalanceRequested,
		AckWait: time.Second, MaxDeliver: 5, MaxAckPending: 8,
		FetchMaxWait: time.Second, DeliverPolicy: nats.DeliverAllPolicy,
		DeliverDecodeErrors: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()

	deliveries, err := consumer.Fetch(ctx, 1)
	if err != nil || len(deliveries) != 1 {
		t.Fatalf("fetch first delivery: count=%d err=%v", len(deliveries), err)
	}
	first := deliveries[0]
	result := handleRebalanceDelivery(ctx, first, s, engine, "trade_rebalance_v1", wakeup)
	if result.Decision != jetstream.ACK || result.Err != nil {
		t.Fatalf("first decision=%v err=%v", result.Decision, result.Err)
	}
	if err = first.Nak(ctx, 0); err != nil {
		t.Fatal(err)
	}
	deliveries, err = consumer.Fetch(ctx, 1)
	if err != nil || len(deliveries) != 1 {
		t.Fatalf("fetch redelivery: count=%d err=%v", len(deliveries), err)
	}
	second := deliveries[0]
	result = handleRebalanceDelivery(ctx, second, s, engine, "trade_rebalance_v1", wakeup)
	if result.Decision != jetstream.ACK || result.Err != nil {
		t.Fatalf("redelivery decision=%v err=%v", result.Decision, result.Err)
	}
	if err = second.Ack(ctx); err != nil {
		t.Fatal(err)
	}

	eventuallyTradeE2E(t, 5*time.Second, func() bool {
		legs, queryErr := s.ListRebalanceLegs(ctx, "space", "strategy-e2e-run:rebalance:execution-e2e")
		if queryErr != nil || len(legs) != 1 || legs[0].PlanID == "" {
			return false
		}
		order, queryErr := s.GetOrder(ctx, "space", legs[0].PlanID)
		return queryErr == nil && order.State == "OPEN"
	})
	var inboxCount, runCount int64
	if err = s.DBForTest().Table("t_trade_inbox").Count(&inboxCount).Error; err != nil {
		t.Fatal(err)
	}
	if err = s.DBForTest().Table("t_rebalance_runs").Count(&runCount).Error; err != nil {
		t.Fatal(err)
	}
	if inboxCount != 1 || runCount != 1 {
		t.Fatalf("inbox=%d runs=%d", inboxCount, runCount)
	}
}

func eventuallyTradeE2E(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not satisfied before timeout")
}
