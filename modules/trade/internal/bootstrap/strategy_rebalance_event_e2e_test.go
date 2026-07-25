package bootstrap

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/command"
	"github.com/mooyang-code/moox/modules/trade/internal/config"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/instrument"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/ledger"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	natsserver "github.com/nats-io/nats-server/v2/server"
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
		LastPrice: shared.MustDecimal("10"),
	}, nil
}
func (strategyTradeE2EAdapter) ListFills(context.Context, string, string) ([]exchange.FillEvent, error) {
	return nil, nil
}
func (strategyTradeE2EAdapter) SubscribePrivate(context.Context, exchange.PrivateEventHandler) error {
	return nil
}

func TestExternalStrategyRebalanceEventCreatesOneRunAndWakesWorker(t *testing.T) {
	ns, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	go ns.Start()
	if !ns.ReadyForConnections(10 * time.Second) {
		ns.Shutdown()
		t.Fatal("cross-module EventBus fixture did not start")
	}
	t.Cleanup(func() {
		ns.Shutdown()
		ns.WaitForShutdown()
	})
	natsURL := ns.ClientURL()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	runExternalStrategyPublisher(t, ctx, natsURL)

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
	engine := &command.Engine{Store: s, Resolver: workerResolver{adapter: strategyTradeE2EAdapter{}}}
	workerCtx, stopWorker := context.WithCancel(ctx)
	defer stopWorker()
	if err = startKernelWorkers(workerCtx, config.EventBusConfig{Enabled: false}, s, engine); err != nil {
		t.Fatal(err)
	}

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
	result := handleRebalanceDelivery(ctx, first, s, engine, "trade_rebalance_v1", nil)
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
	result = handleRebalanceDelivery(ctx, second, s, engine, "trade_rebalance_v1", nil)
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
		return queryErr == nil && order.State == "FILLED"
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

func runExternalStrategyPublisher(t *testing.T, ctx context.Context, natsURL string) {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve trade E2E source path")
	}
	strategyDir := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "strategy"))
	cmd := exec.CommandContext(
		ctx,
		"go", "test", "./test",
		"-tags=e2e_external",
		"-run", "^TestExternalStrategyCommitPublishesRebalance$",
		"-count=1",
	)
	cmd.Dir = strategyDir
	cmd.Env = append(os.Environ(),
		"CGO_ENABLED=1",
		"MOOX_STRATEGY_TRADE_E2E_NATS_URL="+natsURL,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run real Strategy commit/outbox/relay producer: %v\n%s", err, output)
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
