package test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/eventconsumer"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	traderuntime "github.com/mooyang-code/moox/modules/trade/internal/runtime"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	testLogicalAccount = "logical-e2e"
	testRunner         = "runner-e2e"
)

func TestLogicalAccountTargetConsumerPersistsLatestAndWorkerConverges(t *testing.T) {
	f := newFixture(t, exchange.MarketTypeSpot, newFakeExchange(exchange.MarketTypeSpot))
	seedTargetPaperHoldings(t, f)
	seedLogicalAccount(t, f.store)
	var wakes atomic.Int32
	now := time.Now().UTC()
	options := eventconsumer.TargetOptions{
		Store: f.store, Wake: func() { wakes.Add(1) },
		Now:            func() time.Time { return now },
		WeightResolver: testTargetWeightResolver{},
	}

	for _, command := range []struct {
		id       string
		sequence int64
		quantity string
		decision jetstream.HandlerDecision
	}{
		{id: "target-2", sequence: 2, quantity: "0.02", decision: jetstream.ACK},
		{id: "target-stale", sequence: 1, quantity: "9", decision: jetstream.TERM},
		{id: "target-3", sequence: 3, quantity: "0.03", decision: jetstream.ACK},
	} {
		result := eventconsumer.HandleTarget(
			context.Background(),
			targetDelivery(t, now, command.id, command.sequence, command.quantity),
			options,
		)
		require.Equal(t, command.decision, result.Decision, result.Err)
	}

	current, err := f.store.GetLogicalAccountTarget(
		context.Background(),
		testSpace,
		testLogicalAccount,
	)
	require.NoError(t, err)
	require.Equal(t, "target-3", current.TargetID)
	// Modern target events derive the historical ordering value from bar_end_time;
	// command_sequence is deprecated and no longer authorizes execution.
	require.Equal(t, uint64(now.UnixMilli()), current.CommandSequence)
	require.Equal(t, "0.03", current.Targets[0].Quantity)
	require.Equal(t, int32(2), wakes.Load(), "stale command must not wake convergence")

	f.fake.reference = exchange.ReferencePrice{
		Price: shared.MustDecimal("50000"), UpdatedAt: now,
	}
	f.orders.Validator.Now = func() time.Time { return now }
	f.orders.Now = func() time.Time { return now }
	executor := &targetapp.Executor{
		Store: f.store, Orders: f.orders,
		Prices:           targetapp.ExchangePriceSource{Adapters: adapterSource{adapter: f.adapter}},
		Now:              func() time.Time { return now },
		MaxChildNotional: shared.MustDecimal("1000000"),
	}
	worker := &traderuntime.TargetWorker{
		Store: f.store, Executor: executor, Interval: time.Hour,
		Now: func() time.Time { return now },
	}
	workerCtx, cancel := context.WithCancel(context.Background())
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		_ = worker.Run(workerCtx)
	}()
	worker.Wake()
	require.Eventually(t, func() bool {
		f.fake.mu.Lock()
		defer f.fake.mu.Unlock()
		return len(f.fake.requests) == 1
	}, time.Second, 10*time.Millisecond)
	f.fake.mu.Lock()
	child := f.fake.requests[0]
	f.fake.mu.Unlock()
	require.Equal(t, exchange.OrderTypeMarket, child.OrderType)
	require.Equal(t, exchange.SideSell, child.Side)
	require.Equal(t, "9.97", child.Quantity.String())

	_, err = executor.Converge(
		context.Background(),
		testSpace,
		testLogicalAccount,
	)
	require.NoError(t, err)
	f.fake.mu.Lock()
	require.Len(t, f.fake.requests, 1, "active child must suppress duplicate placement")
	f.fake.mu.Unlock()
	cancel()
	<-workerDone
}

func TestLogicalAccountOmittedInstrumentConvergesToZero(t *testing.T) {
	testEmptyFullTargetClosesExistingPosition(t)
}

func TestLogicalAccountEmptyFullFlattensAllPhysicalPositions(t *testing.T) {
	testEmptyFullTargetClosesExistingPosition(t)
}

func testEmptyFullTargetClosesExistingPosition(t *testing.T) {
	now := time.Now().UTC()
	f := newFixture(t, exchange.MarketTypeSpot, newFakeExchange(exchange.MarketTypeSpot))
	seedTargetPaperHoldings(t, f)
	seedLogicalAccount(t, f.store)
	result := eventconsumer.HandleTarget(
		context.Background(),
		targetDeliveryWithTargets(t, now, "target-empty", 1, nil),
		eventconsumer.TargetOptions{Store: f.store, Now: func() time.Time { return now }, WeightResolver: testTargetWeightResolver{}},
	)
	require.Equal(t, jetstream.ACK, result.Decision, result.Err)
	f.fake.reference = exchange.ReferencePrice{
		Price: shared.MustDecimal("50000"), UpdatedAt: now,
	}
	f.orders.Validator.Now = func() time.Time { return now }
	f.orders.Now = func() time.Time { return now }
	executor := targetapp.Executor{
		Store: f.store, Orders: f.orders,
		Prices:           targetapp.ExchangePriceSource{Adapters: adapterSource{adapter: f.adapter}},
		Now:              func() time.Time { return now },
		MaxChildNotional: shared.MustDecimal("1000000"),
	}
	converged, err := executor.Converge(
		context.Background(),
		testSpace,
		testLogicalAccount,
	)
	require.NoError(t, err)
	require.Equal(t, "place", converged.Action)
	f.fake.mu.Lock()
	defer f.fake.mu.Unlock()
	require.Len(t, f.fake.requests, 1)
	require.Equal(t, exchange.SideSell, f.fake.requests[0].Side)
	require.Equal(t, "10", f.fake.requests[0].Quantity.String())
}

func TestLogicalAccountTargetRejectsStaleReferenceBeforePlacement(t *testing.T) {
	now := time.Now().UTC()
	f := newFixture(t, exchange.MarketTypeSpot, newFakeExchange(exchange.MarketTypeSpot))
	seedTargetPaperHoldings(t, f)
	seedLogicalAccount(t, f.store)
	result := eventconsumer.HandleTarget(
		context.Background(),
		targetDelivery(t, now, "stale-price-target", 1, "0.03"),
		eventconsumer.TargetOptions{Store: f.store, Now: func() time.Time { return now }, WeightResolver: testTargetWeightResolver{}},
	)
	require.Equal(t, jetstream.ACK, result.Decision, result.Err)
	f.fake.reference = exchange.ReferencePrice{
		Price: shared.MustDecimal("50000"), UpdatedAt: now.Add(-2 * time.Minute),
	}
	f.orders.Validator.Now = func() time.Time { return now }
	executor := targetapp.Executor{
		Store: f.store, Orders: f.orders,
		Prices:           targetapp.ExchangePriceSource{Adapters: adapterSource{adapter: f.adapter}},
		Now:              func() time.Time { return now },
		MaxChildNotional: shared.MustDecimal("1000000"),
	}
	_, err := executor.Converge(
		context.Background(),
		testSpace,
		testLogicalAccount,
	)
	require.Error(t, err)
	require.True(t, errors.Is(err, orderdomain.ErrInvalidSpec))
	f.fake.mu.Lock()
	require.Empty(t, f.fake.requests)
	f.fake.mu.Unlock()
}

func seedTargetPaperHoldings(t *testing.T, f *fixture) {
	t.Helper()
	// The starting BTC must have matching ledger history, not only a snapshot.
	// A historical buy at 5000 leaves 50000 USDT and 10 BTC from initial cash.
	ctx := context.Background()
	require.NoError(t, f.store.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.CreateOrder(store.OrderRecord{
			SpaceID: testSpace, TradingAccountID: testAccount, OrderID: "historical-buy",
			ClientOrderID: "historical-buy", ExchangeSymbol: testSymbol, OrderType: "MARKET",
			Side: "BUY", Quantity: "10", ReferencePrice: "5000", State: "FILLED",
			FilledQuantity: "10", AveragePrice: "5000",
			OwnerType: "EXTERNAL", OwnerID: "historical-buy",
		}); err != nil {
			return err
		}
		_, err := tx.InsertFill(store.FillRecord{
			SpaceID: testSpace, TradingAccountID: testAccount, OrderID: "historical-buy",
			FillID: "historical-buy-fill", ExchangeTradeID: "historical-buy-fill",
			Price: "5000", Quantity: "10", Fee: "0", SettlementAsset: "USDT",
			TradedAt: testNow.Add(-time.Hour).UnixMilli(),
		})
		return err
	}))
	f.fake.account.Balances[0].Total = shared.MustDecimal("50000")
	f.fake.account.Balances[0].Available = shared.MustDecimal("50000")
	f.fake.account.Equity = shared.MustDecimal("550000")
	f.fake.account.AvailableFunds = shared.MustDecimal("50000")
	require.NoError(t, f.store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpdateTradingAccountSnapshot(testSpace, testAccount, paperSnapshotRecordForTest(f.fake.account))
	}))
}

func seedLogicalAccount(t *testing.T, tradeStore *store.Store) {
	t.Helper()
	account, err := tradeStore.GetTradingAccountByID(context.Background(), testAccount)
	require.NoError(t, err)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: testSpace, LogicalAccountID: testLogicalAccount,
			Name: "E2E logical account", OwnerRunnerID: testRunner,
			OwnerInstanceID: testRunner, OwnerSessionID: "session-e2e",
			ExecutionMode: account.ExecutionMode, MarketType: "SPOT", SettlementAsset: "USDT",
			AutomationState: "PAUSED", PauseReason: "configure",
		}); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: testSpace, LogicalAccountID: testLogicalAccount,
			TradingAccountID: testAccount, Enabled: true, Priority: 1,
		}); err != nil {
			return err
		}
		return tx.SetLogicalAccountAutomation(
			testSpace,
			testLogicalAccount,
			"ACTIVE",
			"",
		)
	}))
}

func targetDelivery(
	t *testing.T,
	now time.Time,
	targetID string,
	sequence int64,
	quantity string,
) *jetstream.Delivery {
	t.Helper()
	return targetDeliveryWithTargets(
		t,
		now,
		targetID,
		sequence,
		[]*tradeeventpb.InstrumentWeightTarget{{
			InstrumentId: "BTC-USDT",
			TargetWeight: quantity,
		}},
	)
}

func targetDeliveryWithTargets(
	t *testing.T,
	now time.Time,
	targetID string,
	sequence int64,
	targets []*tradeeventpb.InstrumentWeightTarget,
) *jetstream.Delivery {
	t.Helper()
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	// Keep every bar at or before the fixture clock while preserving the
	// command sequence ordering used by the stale-target assertions.
	bar := now.Add(time.Duration(sequence-3) * time.Millisecond)
	encoded, err := registry.Encode(
		events.LogicalAccountTargetWeightRequested,
		&tradeeventpb.LogicalAccountTargetWeightRequested{
			TargetId: targetID, RunnerId: testRunner, InstanceId: testRunner,
			SessionId: "session-e2e", StrategyId: "strategy-e2e",
			LogicalAccountId: testLogicalAccount,
			CommandSequence:  sequence, Targets: targets,
			BarEndTime: timestamppb.New(bar), EffectiveAt: timestamppb.New(bar),
			ValidUntil: timestamppb.New(bar.Add(time.Hour)),
		},
		events.PublishOptions{
			EventID: targetID, OccurredAt: now,
			SpaceID: testSpace, SubjectID: testLogicalAccount,
		},
	)
	require.NoError(t, err)
	raw, err := proto.Marshal(encoded.Message)
	require.NoError(t, err)
	return &jetstream.Delivery{
		RawData: raw, Subject: encoded.Subject,
		RawMessageID: targetID, ContentType: events.ContentType,
	}
}
