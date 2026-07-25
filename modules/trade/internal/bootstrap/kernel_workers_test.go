package bootstrap

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/command"
	rebalanceapp "github.com/mooyang-code/moox/modules/trade/internal/application/rebalance"
	"github.com/mooyang-code/moox/modules/trade/internal/config"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/instrument"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/ledger"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/rebalance"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
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
	return instrument.Rules{
		BaseAsset: "BTC", QuoteAsset: "USDT",
		StepSize: shared.MustDecimal("0.001"), LastPrice: shared.MustDecimal("10"),
	}, nil
}
func (w workerStubAdapter) ListFills(context.Context, string, string) ([]exchange.FillEvent, error) {
	return w.fills, nil
}
func (w workerStubAdapter) SubscribePrivate(context.Context, exchange.PrivateEventHandler) error {
	return nil
}

type workerResolver struct {
	adapter exchange.TradingAdapter
	channel exchange.Channel
}

func (r workerResolver) Resolve(context.Context, string, string) (exchange.TradingAdapter, error) {
	return r.adapter, nil
}
func (r workerResolver) ResolvePublic(context.Context, string, string) (exchange.TradingAdapter, error) {
	return r.adapter, nil
}
func (r workerResolver) DescribeChannel(context.Context, string, string) (exchange.Channel, error) {
	if r.channel.MarketType == "" {
		return exchange.Channel{AccountID: "acct", MarketType: "spot", IsSimulated: true}, nil
	}
	return r.channel, nil
}

func openWorkerStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func seedBalance(t *testing.T, s *store.Store, asset string, amount shared.Decimal) {
	t.Helper()
	require.NoError(t, s.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.PostLedger("space", ledger.Transaction{
			ID: shared.LedgerTransactionID("seed-" + asset), BizType: "seed", RefType: "test", RefID: asset,
			Entries: []ledger.Entry{
				{AccountID: "clearing", Asset: asset, Bucket: "clearing", Amount: amount.Neg()},
				{AccountID: "acct", Asset: asset, Bucket: "available", Amount: amount},
			},
		})
	}))
}

func rebalanceDelivery(t *testing.T, eventID, subjectID string, request *tradeeventpb.RebalanceRequested) *jetstream.Delivery {
	t.Helper()
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	encoded, err := registry.Encode(events.TradeRebalanceRequested, request, events.PublishOptions{
		EventID: eventID, OccurredAt: time.Now().UTC(), SpaceID: "space", SubjectID: subjectID,
	})
	require.NoError(t, err)
	raw, err := proto.Marshal(encoded.Message)
	require.NoError(t, err)
	return &jetstream.Delivery{
		RawData: raw, RawMessageID: eventID, Subject: encoded.Subject,
		ContentType: events.ContentType, DeliveryCount: 1,
	}
}

func validRebalanceRequest() *tradeeventpb.RebalanceRequested {
	return &tradeeventpb.RebalanceRequested{
		RequestId: "request-1", StrategyRunId: "strategy-run-1", ExecutionBindingId: "execution-1",
		AccountId: "acct", ChannelId: "chan", Mode: "paper", DataRevision: "revision-1",
		CapitalAmount: "100", QuoteAsset: "USDT", CommandSequence: 1,
		Targets: []*tradeeventpb.RebalanceTarget{{
			InstrumentId: "BTC-USDT", Symbol: "BTC-USDT", MarketType: "spot", TargetWeight: "0.5",
		}},
	}
}

func TestHandleRebalanceDeliveryAcknowledgesStaleSequenceWithoutCreatingRun(t *testing.T) {
	ctx := context.Background()
	s := openWorkerStore(t)
	seedBalance(t, s, "USDT", shared.MustDecimal("100"))
	engine := &command.Engine{Store: s, Resolver: workerResolver{adapter: workerStubAdapter{}}}

	newer := validRebalanceRequest()
	newer.RequestId = "request-2"
	newer.StrategyRunId = "strategy-run-2"
	newer.CommandSequence = 2
	result := handleRebalanceDelivery(ctx, rebalanceDelivery(t, newer.GetRequestId(), newer.GetExecutionBindingId(), newer), s, engine, "trade_rebalance_v1", nil)
	require.Equal(t, jetstream.ACK, result.Decision)
	require.NoError(t, result.Err)

	stale := validRebalanceRequest()
	result = handleRebalanceDelivery(ctx, rebalanceDelivery(t, stale.GetRequestId(), stale.GetExecutionBindingId(), stale), s, engine, "trade_rebalance_v1", nil)
	require.Equal(t, jetstream.ACK, result.Decision)
	require.NoError(t, result.Err)

	var runCount, inboxCount int64
	require.NoError(t, s.DBForTest().Table("t_rebalance_runs").Count(&runCount).Error)
	require.NoError(t, s.DBForTest().Table("t_trade_inbox").Count(&inboxCount).Error)
	assert.Equal(t, int64(1), runCount)
	assert.Equal(t, int64(2), inboxCount)
}

func TestHandleRebalanceDeliveryCreatesRunAndDeduplicates(t *testing.T) {
	ctx := context.Background()
	s := openWorkerStore(t)
	seedBalance(t, s, "USDT", shared.MustDecimal("100"))
	engine := &command.Engine{Store: s, Resolver: workerResolver{adapter: workerStubAdapter{}}}

	request := validRebalanceRequest()
	delivery := rebalanceDelivery(t, request.GetRequestId(), request.GetExecutionBindingId(), request)
	first := handleRebalanceDelivery(ctx, delivery, s, engine, "trade_rebalance_v1", newKernelWakeup())
	assert.Equal(t, jetstream.ACK, first.Decision)
	second := handleRebalanceDelivery(ctx, delivery, s, &command.Engine{Store: s}, "trade_rebalance_v1", newKernelWakeup())
	assert.Equal(t, jetstream.ACK, second.Decision)

	runs, err := s.ListActiveRebalanceRuns(ctx, 10)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, request.GetRequestId(), runs[0].RunID)
	var inboxCount int64
	require.NoError(t, s.DBForTest().Table("t_trade_inbox").Count(&inboxCount).Error)
	assert.Equal(t, int64(1), inboxCount)
}

func TestTradeSnapshotResolverRejectsModeChannelMismatch(t *testing.T) {
	for name, tc := range map[string]struct {
		mode    string
		channel exchange.Channel
	}{
		"paper on real channel": {
			mode: "paper", channel: exchange.Channel{AccountID: "acct", MarketType: "spot"},
		},
		"live on simulated channel": {
			mode: "live", channel: exchange.Channel{AccountID: "acct", MarketType: "spot", IsSimulated: true},
		},
	} {
		t.Run(name, func(t *testing.T) {
			resolver := tradeSnapshotResolver{engine: &command.Engine{
				Resolver: workerResolver{adapter: workerStubAdapter{}, channel: tc.channel},
			}}
			_, err := resolver.ResolveChannel(context.Background(), "space", "acct", "chan", tc.mode)
			require.Error(t, err)
			assert.True(t, rebalanceapp.IsPermanentRequestError(err))
		})
	}
}

func TestPaperRebalanceFillsWithoutPriorTradeOrRealExchangeCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := openWorkerStore(t)
	seedBalance(t, s, "USDT", shared.MustDecimal("100"))
	adapter := &uniquePaperSafetyAdapter{}
	engine := &command.Engine{Store: s, Resolver: workerResolver{adapter: adapter}}
	require.NoError(t, startKernelWorkers(ctx, config.EventBusConfig{Enabled: false}, s, engine))

	request := validRebalanceRequest()
	delivery := rebalanceDelivery(t, request.GetRequestId(), request.GetExecutionBindingId(), request)
	result := handleRebalanceDelivery(ctx, delivery, s, engine, "trade_rebalance_v1", nil)
	require.Equal(t, jetstream.ACK, result.Decision)
	require.NoError(t, result.Err)

	require.Eventually(t, func() bool {
		legs, err := s.ListRebalanceLegs(ctx, "space", request.GetRequestId())
		if err != nil || len(legs) != 1 || legs[0].PlanID == "" {
			return false
		}
		current, err := s.GetOrder(ctx, "space", legs[0].PlanID)
		return err == nil && current.State == "FILLED"
	}, 3*time.Second, 10*time.Millisecond)
	assert.Zero(t, adapter.placeCalls)
}

type uniquePaperSafetyAdapter struct {
	workerStubAdapter
	placeCalls int
}

func (a *uniquePaperSafetyAdapter) Place(context.Context, exchange.PlaceRequest) (exchange.ExchangeOrderResult, error) {
	a.placeCalls++
	return exchange.ExchangeOrderResult{}, errors.New("real exchange must not be called for paper")
}

func TestHandleRebalanceDeliveryRejectsPermanentContractError(t *testing.T) {
	s := openWorkerStore(t)
	request := validRebalanceRequest()
	delivery := rebalanceDelivery(t, request.GetRequestId(), request.GetExecutionBindingId(), request)
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	delivery.Subject, err = registry.RenderSubject(events.TradeRebalanceRequested, "space", "wrong-binding")
	require.NoError(t, err)
	result := handleRebalanceDelivery(context.Background(), delivery, s, &command.Engine{Store: s, Adapter: workerStubAdapter{}}, "trade_rebalance_v1", nil)
	assert.Equal(t, jetstream.TERM, result.Decision)
	assert.Error(t, result.Err)
}

func TestHandleRebalanceDeliveryRetriesSnapshotFailure(t *testing.T) {
	s := openWorkerStore(t)
	request := validRebalanceRequest()
	delivery := rebalanceDelivery(t, request.GetRequestId(), request.GetExecutionBindingId(), request)
	result := handleRebalanceDelivery(context.Background(), delivery, s, &command.Engine{Store: s, Adapter: workerStubAdapter{}}, "trade_rebalance_v1", nil)
	assert.Equal(t, jetstream.RETRY, result.Decision)
	assert.Error(t, result.Err)
}

func TestReconcileOrdersOnceAppliesFills(t *testing.T) {
	s := openWorkerStore(t)
	seedBalance(t, s, "USDT", shared.MustDecimal("100"))
	adapter := workerStubAdapter{fills: []exchange.FillEvent{{
		ExchangeTradeID: "fill-1", Symbol: "BTC-USDT", Side: "BUY",
		BaseAsset: "BTC", QuoteAsset: "USDT",
		Quantity: shared.MustDecimal("1"), Price: shared.MustDecimal("10"), Fee: shared.Zero(),
	}}}
	engine := &command.Engine{Store: s, Adapter: adapter}
	placed, err := engine.Place(context.Background(), command.PlaceInput{
		SpaceID: "space", OrderID: "ord-1", ClientOrderID: "cli-1",
		AccountID: "acct", ChannelID: "chan", Symbol: "BTC-USDT",
		MarketType: "spot", BaseAsset: "BTC", QuoteAsset: "USDT",
		Side: "BUY", Quantity: "1", Price: "10",
	})
	require.NoError(t, err)
	_, err = engine.Submit(context.Background(), "space", placed.OrderID, "")
	require.NoError(t, err)
	require.NoError(t, reconcileOrdersOnce(context.Background(), s, engine, "space", "acct", "chan"))
	got, err := s.GetOrder(context.Background(), "space", placed.OrderID)
	require.NoError(t, err)
	assert.Equal(t, "1", got.FilledQuantity)
}

func TestAdvanceActiveRebalancesAdvancesRun(t *testing.T) {
	ctx := context.Background()
	s := openWorkerStore(t)
	seedBalance(t, s, "BTC", shared.MustDecimal("1"))
	engine := &command.Engine{Store: s, Adapter: workerStubAdapter{}}
	svc := rebalanceapp.Service{Store: s, Engine: engine}
	require.NoError(t, svc.Create(ctx, rebalanceapp.CreateInput{
		SpaceID: "space", RunID: "run-1", IdempotencyKey: "idem", AccountID: "acct", ChannelID: "chan",
		MarketSnapshotID: "m1", PositionSnapshotID: "p1", RulesVersion: "r1",
		Mode:     rebalance.FullTarget,
		Targets:  []rebalance.Target{{Symbol: "BTC-USDT", Quantity: shared.Zero()}},
		Currents: []rebalance.Current{{Symbol: "BTC-USDT", Quantity: shared.MustDecimal("1")}},
		Markets: map[string]rebalanceapp.Market{
			"BTC-USDT": {BaseAsset: "BTC", QuoteAsset: "USDT", Price: "10", MarketType: "spot"},
		},
	}))
	require.NoError(t, advanceActiveRebalances(ctx, s, engine))
	legs, err := s.ListRebalanceLegs(ctx, "space", "run-1")
	require.NoError(t, err)
	assert.NotEmpty(t, legs)
}

func TestStartKernelWorkersDisabledStartsLocalWorker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := openWorkerStore(t)
	err := startKernelWorkers(ctx, config.EventBusConfig{Enabled: false}, s, &command.Engine{Store: s, Adapter: workerStubAdapter{}})
	require.NoError(t, err)
	cancel()
}

func TestKernelEventBusReadyWithNilClient(t *testing.T) {
	setKernelEventBusClient(nil)
	assert.False(t, kernelEventBusReady())
}

func TestRegisterMetricsReporterNilServer(t *testing.T) {
	registerMetricsReporter(nil)
}
