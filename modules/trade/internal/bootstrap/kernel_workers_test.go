package bootstrap

import (
	"context"
	"encoding/json"
	"github.com/mooyang-code/moox/modules/trade/internal/application/command"
	rebalanceapp "github.com/mooyang-code/moox/modules/trade/internal/application/rebalance"
	"github.com/mooyang-code/moox/modules/trade/internal/config"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/instrument"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/ledger"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/rebalance"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/messagepb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
	"path/filepath"
	"testing"
)

type workerStubAdapter struct {
	fills []exchange.FillEvent
}

func (w workerStubAdapter) Place(context.Context, exchange.PlaceRequest) (exchange.ExchangeOrderResult, error) {
	return exchange.ExchangeOrderResult{ExchangeOrderID: "ex-worker", Status: "OPEN"}, nil
}
func (w workerStubAdapter) Cancel(context.Context, string, string) (exchange.ExchangeOrderResult, error) {
	return exchange.ExchangeOrderResult{Status: "CANCELED"}, nil
}
func (w workerStubAdapter) QueryByClientOrderID(context.Context, string, string) (exchange.ExchangeOrderResult, error) {
	return exchange.ExchangeOrderResult{Status: "OPEN"}, nil
}
func (w workerStubAdapter) Rules(context.Context, string) (instrument.Rules, error) {
	return instrument.Rules{BaseAsset: "BTC", QuoteAsset: "USDT"}, nil
}
func (w workerStubAdapter) ListFills(context.Context, string, string) ([]exchange.FillEvent, error) {
	return w.fills, nil
}
func (w workerStubAdapter) SubscribePrivate(context.Context, exchange.PrivateEventHandler) error {
	return nil
}

func openKernelOrder(t *testing.T, s *store.Store, e *command.Engine) store.OrderRecord {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *store.Tx) error {
		return tx.PostLedger("space", ledger.Transaction{
			ID: shared.LedgerTransactionID("seed"), BizType: "seed", RefType: "test", RefID: "1",
			Entries: []ledger.Entry{
				{AccountID: "clearing", Asset: "USDT", Bucket: "clearing", Amount: shared.MustDecimal("100").Neg()},
				{AccountID: "acct", Asset: "USDT", Bucket: "available", Amount: shared.MustDecimal("100")},
			},
		})
	}))
	placed, err := e.Place(ctx, command.PlaceInput{
		SpaceID: "space", OrderID: "ord-1", ClientOrderID: "cli-1",
		AccountID: "acct", ChannelID: "chan", Symbol: "BTC-USDT",
		MarketType: "spot", BaseAsset: "BTC", QuoteAsset: "USDT",
		Side: "BUY", Quantity: "1", Price: "10",
	})
	require.NoError(t, err)
	submitted, err := e.Submit(ctx, "space", placed.OrderID, "")
	require.NoError(t, err)
	return submitted
}

func wrapDelivery(t *testing.T, payload []byte, topic, messageID string) *jetstream.Delivery {
	t.Helper()
	wrapped, err := proto.Marshal(&wrapperspb.BytesValue{Value: payload})
	require.NoError(t, err)
	return &jetstream.Delivery{
		Message: &messagepb.MooxMessage{Payload: wrapped, Topic: topic, MessageId: messageID},
	}
}

func TestReconcileOrdersOnce_WithFills_ShouldApply(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	adapter := workerStubAdapter{fills: []exchange.FillEvent{{
		ExchangeTradeID: "fill-1", Symbol: "BTC-USDT", Side: "BUY",
		BaseAsset: "BTC", QuoteAsset: "USDT",
		Quantity: shared.MustDecimal("1"), Price: shared.MustDecimal("10"), Fee: shared.Zero(),
	}}}
	engine := &command.Engine{Store: s, Adapter: adapter}
	rec := openKernelOrder(t, s, engine)
	require.NoError(t, reconcileOrdersOnce(context.Background(), s, engine, "space", "acct", "chan"))
	got, err := s.GetOrder(context.Background(), "space", rec.OrderID)
	require.NoError(t, err)
	assert.Equal(t, "1", got.FilledQuantity)
}

func TestAdvanceActiveRebalances_ActiveRun_ShouldAdvance(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	require.NoError(t, s.Transaction(ctx, func(tx *store.Tx) error {
		return tx.PostLedger("space", ledger.Transaction{
			ID: shared.LedgerTransactionID("seed-btc"), BizType: "seed", RefType: "test", RefID: "btc",
			Entries: []ledger.Entry{
				{AccountID: "clearing", Asset: "BTC", Bucket: "clearing", Amount: shared.MustDecimal("1").Neg()},
				{AccountID: "acct", Asset: "BTC", Bucket: "available", Amount: shared.MustDecimal("1")},
			},
		})
	}))
	engine := &command.Engine{Store: s, Adapter: workerStubAdapter{}}
	svc := rebalanceapp.Service{Store: s, Engine: engine}
	require.NoError(t, svc.Create(ctx, rebalanceapp.CreateInput{
		SpaceID: "space", RunID: "run-1", IdempotencyKey: "idem", AccountID: "acct", ChannelID: "chan",
		MarketSnapshotID: "m1", PositionSnapshotID: "p1", RulesVersion: "r1",
		Mode:     rebalance.FullTarget,
		Targets:  []rebalance.Target{{Symbol: "BTCUSDT", Quantity: shared.Zero()}},
		Currents: []rebalance.Current{{Symbol: "BTCUSDT", Quantity: shared.MustDecimal("1")}},
		Markets:  map[string]rebalanceapp.Market{"BTCUSDT": {BaseAsset: "BTC", QuoteAsset: "USDT", Price: "10"}},
	}))
	require.NoError(t, advanceActiveRebalances(ctx, s, engine))
	legs, err := s.ListRebalanceLegs(ctx, "space", "run-1")
	require.NoError(t, err)
	assert.NotEmpty(t, legs)
}

func TestHandleExecutionDelivery_ReadyOrder_ShouldSubmit(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	engine := &command.Engine{Store: s, Adapter: workerStubAdapter{}}
	require.NoError(t, s.ReconcileBalances(context.Background(), "space", "acct", map[string]map[string]shared.Decimal{
		"USDT": {"available": shared.MustDecimal("20")},
	}))
	placed, err := engine.Place(context.Background(), command.PlaceInput{
		SpaceID: "space", OrderID: "exec-1", ClientOrderID: "exec-cli",
		AccountID: "acct", ChannelID: "chan", Symbol: "BTC-USDT",
		MarketType: "spot", BaseAsset: "BTC", QuoteAsset: "USDT",
		Side: "BUY", Quantity: "1", Price: "10",
	})
	require.NoError(t, err)
	payload, err := json.Marshal(placed)
	require.NoError(t, err)
	delivery := wrapDelivery(t, payload, "moox.trade.execution.slice_ready.v1", "msg-1")
	require.NoError(t, handleExecutionDelivery(context.Background(), delivery, s, engine, "exec-consumer"))
	got, err := s.GetOrder(context.Background(), "space", "exec-1")
	require.NoError(t, err)
	assert.Equal(t, "OPEN", got.State)
}

func TestHandleRebalanceDelivery_ActiveRun_ShouldAdvance(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	require.NoError(t, s.Transaction(ctx, func(tx *store.Tx) error {
		return tx.PostLedger("space", ledger.Transaction{
			ID: shared.LedgerTransactionID("seed-btc"), BizType: "seed", RefType: "test", RefID: "btc",
			Entries: []ledger.Entry{
				{AccountID: "clearing", Asset: "BTC", Bucket: "clearing", Amount: shared.MustDecimal("1").Neg()},
				{AccountID: "acct", Asset: "BTC", Bucket: "available", Amount: shared.MustDecimal("1")},
			},
		})
	}))
	engine := &command.Engine{Store: s, Adapter: workerStubAdapter{}}
	svc := rebalanceapp.Service{Store: s, Engine: engine}
	require.NoError(t, svc.Create(ctx, rebalanceapp.CreateInput{
		SpaceID: "space", RunID: "run-2", IdempotencyKey: "idem2", AccountID: "acct", ChannelID: "chan",
		MarketSnapshotID: "m1", PositionSnapshotID: "p1", RulesVersion: "r1", Mode: rebalance.FullTarget,
		Targets:  []rebalance.Target{{Symbol: "BTCUSDT", Quantity: shared.Zero()}},
		Currents: []rebalance.Current{{Symbol: "BTCUSDT", Quantity: shared.MustDecimal("1")}},
		Markets:  map[string]rebalanceapp.Market{"BTCUSDT": {BaseAsset: "BTC", QuoteAsset: "USDT", Price: "10"}},
	}))
	runRows, err := s.ListActiveRebalanceRuns(ctx, 10)
	require.NoError(t, err)
	require.NotEmpty(t, runRows)
	payload, err := json.Marshal(runRows[0])
	require.NoError(t, err)
	delivery := wrapDelivery(t, payload, "moox.trade.rebalance.requested.v1", "msg-2")
	require.NoError(t, handleRebalanceDelivery(ctx, delivery, s, engine, "rebalance-consumer"))
	legs, err := s.ListRebalanceLegs(ctx, "space", "run-2")
	require.NoError(t, err)
	assert.NotEmpty(t, legs)
}

func TestStartKernelWorkers_Disabled_ShouldNoop(t *testing.T) {
	err := startKernelWorkers(context.Background(), config.EventBusConfig{Enabled: false}, nil, nil)
	assert.NoError(t, err)
}

func TestDeliveryTraceContext_WithTrace_ShouldInjectTelemetry(t *testing.T) {
	delivery := &jetstream.Delivery{
		Message: &messagepb.MooxMessage{
			Trace: &messagepb.TraceContext{TraceId: "trace-1", RequestId: "req-1"},
		},
	}
	ctx := deliveryTraceContext(context.Background(), delivery)
	// Context should differ when trace is present.
	assert.NotEqual(t, context.Background(), ctx)
}

func TestDeliveryTraceContext_NilDelivery_ShouldReturnOriginal(t *testing.T) {
	ctx := context.Background()
	assert.Equal(t, ctx, deliveryTraceContext(ctx, nil))
}

func TestKernelEventBusReady_WithNilClient_ShouldReturnFalse(t *testing.T) {
	setKernelEventBusClient(nil)
	assert.False(t, kernelEventBusReady())
}

func TestRegisterMetricsReporter_NilServer_ShouldNoop(t *testing.T) {
	registerMetricsReporter(nil)
}
