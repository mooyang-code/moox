package test

import (
	"context"
	"errors"
	"testing"
	"time"

	equityapp "github.com/mooyang-code/moox/modules/trade/internal/application/equity"
	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/eventconsumer"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	paperexec "github.com/mooyang-code/moox/modules/trade/internal/execution/paper"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestTargetLiveBalanceRejectionRefreshesBeforeRoutingNextChildE2E(t *testing.T) {
	ctx := context.Background()
	a := newFakeExchange(exchange.MarketTypeSpot)
	f := newFixtureWithMode(t, exchange.MarketTypeSpot, a, exchange.ExecutionModeLive)
	b := newFakeExchange(exchange.MarketTypeSpot)
	const second = "live-capacity-b"
	account, err := f.store.GetTradingAccountByID(ctx, testAccount)
	require.NoError(t, err)
	account.TradingAccountID, account.Name = second, second
	account.Snapshot.Balances = []store.AssetBalance{{Asset: "USDT", Available: "100000", Total: "100000"}}
	require.NoError(t, f.store.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(account); err != nil {
			return err
		}
		if err := tx.UpdateTradingAccountFacts(testSpace, testAccount, nil, account.Snapshot, testNow.UnixMilli(), testNow.UnixMilli()); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: testSpace, LogicalAccountID: testLogicalAccount, Name: "Live capacity routing",
			OwnerInstanceID: "live-instance", OwnerSessionID: "live-session",
			ExecutionMode: "LIVE", MarketType: "SPOT", SettlementAsset: "USDT",
			AutomationState: "PAUSED", PauseReason: "configure",
		}); err != nil {
			return err
		}
		for i, id := range []string{testAccount, second} {
			if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{SpaceID: testSpace,
				LogicalAccountID: testLogicalAccount, TradingAccountID: id, Enabled: true, Priority: i + 1}); err != nil {
				return err
			}
		}
		return tx.SetLogicalAccountAutomation(testSpace, testLogicalAccount, "ACTIVE", "")
	}))
	_, accepted, err := f.store.AcceptLogicalAccountTarget(ctx, store.LogicalAccountTargetRecord{
		SpaceID: testSpace, LogicalAccountID: testLogicalAccount, TargetID: "live-target",
		InstanceID: "live-instance", SessionID: "live-session", StrategyID: "live-strategy",
		BarEndTime: testNow.UnixMilli(), EffectiveAt: testNow.UnixMilli(), ValidUntil: testNow.Add(time.Hour).UnixMilli(),
		Targets: []store.InstrumentTarget{{InstrumentID: testInstrumentID, Quantity: "1"}},
		Status:  targetapp.StatusPending, AcceptedAt: testNow.UnixMilli(),
	})
	require.NoError(t, err)
	require.True(t, accepted)
	adapters := isolationAdapters{testAccount: a, second: b}
	f.orders.Adapters, f.sync.Adapters = adapters, adapters
	f.sync.Now = func() time.Time { return testNow }
	a.account.Balances = []exchange.AssetBalance{{Asset: "USDT", Available: shared.Zero(), Total: shared.Zero()}}
	a.account.AvailableFunds = shared.Zero()
	a.placeErr = &exchange.Error{Kind: exchange.ErrorInsufficientBalance, Err: errors.New("authoritative funds exhausted")}
	executor := &targetapp.Executor{Store: f.store, Orders: f.orders,
		Prices: targetapp.ExchangePriceSource{Adapters: adapters}, Now: func() time.Time { return testNow }}
	_, err = executor.Converge(ctx, testSpace, testLogicalAccount)
	require.True(t, exchange.IsKind(err, exchange.ErrorInsufficientBalance), err)
	orders, count, err := f.store.ListOrders(ctx, testSpace, store.OrderQuery{Limit: 10})
	require.NoError(t, err)
	require.Equal(t, int64(1), count, "one child per convergence even after a definitive rejection")
	require.Equal(t, "REJECTED", orders[0].State)
	require.Equal(t, "0", orders[0].RemainingReservedQuantity)
	refreshed, err := f.store.GetTradingAccountByID(ctx, testAccount)
	require.NoError(t, err)
	require.Equal(t, "0", refreshed.Snapshot.AvailableFunds, "definitive remote capacity rejection must refresh local facts")
	result, err := executor.Converge(ctx, testSpace, testLogicalAccount)
	require.NoError(t, err)
	require.Equal(t, "place", result.Action)
	orders, count, err = f.store.ListOrders(ctx, testSpace, store.OrderQuery{Limit: 10})
	require.NoError(t, err)
	require.Equal(t, int64(2), count)
	for _, child := range orders {
		if child.State != "REJECTED" {
			require.Equal(t, second, child.TradingAccountID)
			require.Equal(t, "OPEN", child.State)
			require.Equal(t, "1", child.Quantity)
		}
	}
	require.Equal(t, 1, a.placeCalls)
	require.Equal(t, 1, b.placeCalls)
}

func TestLiveBalanceRejectionRefreshDeadlineIncludesMembershipLockE2E(t *testing.T) {
	fake := newFakeExchange(exchange.MarketTypeSpot)
	f := newFixtureWithMode(t, exchange.MarketTypeSpot, fake, exchange.ExecutionModeLive)
	pending, err := f.orders.Place(context.Background(), testSpace, marketSpec("bounded-rejection", exchange.SideBuy, "0.01"))
	require.NoError(t, err)
	fake.placeErr = &exchange.Error{Kind: exchange.ErrorInsufficientBalance}
	unlock := f.store.LockLogicalAccountMembership()
	held := true
	release := func() {
		if held {
			held = false
			unlock()
		}
	}
	t.Cleanup(release)
	done := make(chan error, 1)
	go func() {
		_, err := f.orders.Submit(context.Background(), testSpace, string(pending.ID))
		done <- err
	}()
	select {
	case err := <-done:
		require.True(t, exchange.IsKind(err, exchange.ErrorInsufficientBalance), err)
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(6 * time.Second):
		release()
		<-done
		t.Fatal("rejection refresh waited beyond its completion deadline for a held membership lock")
	}
	require.True(t, held, "Submit must return while the competing lock remains held")
	account, err := f.store.GetTradingAccountByID(context.Background(), testAccount)
	require.NoError(t, err)
	require.False(t, account.Ready, "a failed refresh must not advertise the disproven capacity snapshot as executable")
	release()
	stored, err := f.store.GetOrder(context.Background(), testSpace, string(pending.ID))
	require.NoError(t, err)
	require.Equal(t, "REJECTED", stored.State)
	require.Equal(t, "0", stored.RemainingReservedQuantity)
}

// This is an in-process integration test using actual Paper, valuation,
// reservation and reducer code. It does not claim production Broker coverage.
func TestTargetCapacitySplitUsesActualPaperFundsAndFillsE2E(t *testing.T) {
	ctx := context.Background()
	f := newProductionPaperFixture(t, exchange.MarketTypeSpot)
	const second = "paper-capacity-b"
	account, err := f.store.GetTradingAccountByID(ctx, testAccount)
	require.NoError(t, err)
	account.TradingAccountID, account.Name = second, second
	account.PaperConfig = nil
	require.NoError(t, f.store.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(account); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: testSpace, LogicalAccountID: testLogicalAccount, Name: "Capacity routing",
			OwnerInstanceID: "capacity-instance", OwnerSessionID: "capacity-session",
			ExecutionMode: "PAPER", MarketType: "SPOT", SettlementAsset: "USDT",
			AutomationState: "PAUSED", PauseReason: "configure",
		}); err != nil {
			return err
		}
		for i, id := range []string{testAccount, second} {
			if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
				SpaceID: testSpace, LogicalAccountID: testLogicalAccount,
				TradingAccountID: id, Enabled: true, Priority: i + 1,
			}); err != nil {
				return err
			}
		}
		return tx.SetLogicalAccountAutomation(testSpace, testLogicalAccount, "ACTIVE", "")
	}))
	account, err = f.store.GetTradingAccountByID(ctx, second)
	require.NoError(t, err)
	adapters := isolationAdapters{
		testAccount: f.adapter,
		second:      &paperexec.Adapter{Account: account, Store: f.store, MarketData: f.fake, Now: func() time.Time { return testNow }},
	}
	_, err = adapters[second].(*paperexec.Adapter).LoadInstruments(ctx)
	require.NoError(t, err)
	f.orders.Adapters, f.sync.Adapters = adapters, adapters
	f.sync.Now = func() time.Time { return testNow }
	for _, id := range []string{testAccount, second} {
		_, err = f.sync.SyncAccount(ctx, id)
		require.NoError(t, err)
	}
	equity := &equityapp.Service{Store: f.store, Adapters: adapters, Now: func() time.Time { return testNow }}
	for _, id := range []string{testAccount, second} {
		require.NoError(t, equity.SampleAccount(ctx, id))
	}
	prices := targetapp.ExchangePriceSource{Adapters: adapters}
	opts := eventconsumer.TargetOptions{
		Store: f.store, Now: func() time.Time { return testNow },
		WeightResolver: &targetapp.WeightResolver{Store: f.store, Prices: prices, Equity: equity, Now: func() time.Time { return testNow }},
	}
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	encoded, err := registry.Encode(events.LogicalAccountTargetWeightRequested, &tradeeventpb.LogicalAccountTargetWeightRequested{
		TargetId: "capacity-target", LogicalAccountId: testLogicalAccount,
		InstanceId: "capacity-instance", SessionId: "capacity-session", StrategyId: "capacity-strategy",
		BarEndTime: timestamppb.New(testNow), EffectiveAt: timestamppb.New(testNow), ValidUntil: timestamppb.New(testNow.Add(time.Hour)),
		Targets: []*tradeeventpb.InstrumentWeightTarget{{InstrumentId: testInstrumentID, TargetWeight: "1"}},
	}, events.PublishOptions{EventID: "capacity-target", OccurredAt: testNow, SpaceID: testSpace, SubjectID: testLogicalAccount})
	require.NoError(t, err)
	raw, err := proto.Marshal(encoded.Message)
	require.NoError(t, err)
	delivery := &jetstream.Delivery{RawData: raw, Subject: encoded.Subject, RawMessageID: "capacity-target", ContentType: events.ContentType}
	accepted := eventconsumer.HandleTarget(ctx, delivery, opts)
	require.Equal(t, jetstream.ACK, accepted.Decision, accepted.Err)
	receipt, err := f.store.GetTargetReceipt(ctx, testSpace, "capacity-target")
	require.NoError(t, err)
	current, err := f.store.GetLogicalAccountTarget(ctx, testSpace, testLogicalAccount)
	require.NoError(t, err)
	require.Equal(t, "4", current.Targets[0].Quantity)
	executor := &targetapp.Executor{Store: f.store, Orders: f.orders, Prices: prices, Now: func() time.Time { return testNow }}
	matcher := &paperexec.Matcher{Store: f.store, Reducer: f.reducer,
		DecideContext: (&paperexec.Decider{Store: f.store, Adapters: adapters, Now: func() time.Time { return testNow }}).Decide,
		Refresh: func(ctx context.Context, id string) error {
			snapshot, err := adapters[id].GetAccountSnapshot(ctx)
			if err != nil {
				return err
			}
			return f.store.Transaction(ctx, func(tx *store.Tx) error {
				return tx.UpdateTradingAccountFacts(testSpace, id, nil, paperSnapshotRecordForTest(snapshot), testNow.UnixMilli(), testNow.UnixMilli())
			})
		},
	}
	for i, id := range []string{testAccount, second} {
		result, err := executor.Converge(ctx, testSpace, testLogicalAccount)
		require.NoError(t, err)
		require.Equal(t, "place", result.Action)
		orders, total, err := f.store.ListOrders(ctx, testSpace, store.OrderQuery{Limit: 10})
		require.NoError(t, err)
		require.Equal(t, int64(i+1), total)
		var child store.OrderRecord
		for _, value := range orders {
			if value.TradingAccountID == id {
				child = value
			}
		}
		require.Equal(t, "2", child.Quantity)
		require.Equal(t, "BUY", child.Side)
		_, err = executor.Converge(ctx, testSpace, testLogicalAccount)
		require.NoError(t, err)
		_, total, err = f.store.ListOrders(ctx, testSpace, store.OrderQuery{Limit: 10})
		require.NoError(t, err)
		require.Equal(t, int64(i+1), total, "active child must prevent another reservation")
		require.NoError(t, matcher.Scan(ctx))
		balance, err := f.store.GetPaperBalanceSnapshot(ctx, testSpace, id)
		require.NoError(t, err)
		require.True(t, balance.Totals["USDT"].IsZero())
		require.Equal(t, "2", balance.Totals["BTC"].String())
	}
	result, err := executor.Converge(ctx, testSpace, testLogicalAccount)
	require.NoError(t, err)
	require.Equal(t, targetapp.StatusConverged, result.Status)
	f.fake.mu.Lock()
	f.fake.reference.Price = f.fake.reference.Price.Add(f.fake.reference.Price)
	f.fake.mu.Unlock()
	replayed := eventconsumer.HandleTarget(ctx, delivery, opts)
	require.Equal(t, jetstream.ACK, replayed.Decision, replayed.Err)
	after, err := f.store.GetTargetReceipt(ctx, testSpace, "capacity-target")
	require.NoError(t, err)
	require.Equal(t, receipt, after, "routing/fill/replay must not revalue the accepted target")
	_, total, err := f.store.ListFills(ctx, testSpace, store.FillQuery{Limit: 10})
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
}
