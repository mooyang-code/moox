package test

import (
	"context"
	"errors"
	"testing"
	"time"

	papersimulation "github.com/mooyang-code/moox/modules/trade/internal/application/papersimulation"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	paperexec "github.com/mooyang-code/moox/modules/trade/internal/execution/paper"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

func TestClosePaperSimulationCancelsReservationsAndSurvivesRestartE2E(t *testing.T) {
	ctx := context.Background()
	f := newRestingPaperFixture(t, exchange.MarketTypeSpot)
	require.NoError(t, f.store.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: testSpace, LogicalAccountID: "logical-close", Name: "close",
			ExecutionMode: "PAPER", MarketType: "SPOT", SettlementAsset: "USDT",
			AutomationState: "PAUSED", PauseReason: "test setup",
		}); err != nil {
			return err
		}
		return tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: testSpace, LogicalAccountID: "logical-close",
			TradingAccountID: testAccount, Enabled: true, Priority: 1,
		})
	}))

	spec := marketSpec("close-open-order", exchange.SideBuy, "0.01")
	spec.Type = exchange.OrderTypeLimit
	spec.FillPolicy = exchange.FillPolicyGTC
	limit := shared.MustDecimal("49000")
	spec.LimitPrice = &limit
	placed := mustPlace(t, f, spec)
	open, err := f.orders.Submit(ctx, testSpace, string(placed.ID))
	require.NoError(t, err)
	require.Equal(t, orderdomain.Open, open.State)
	snapshot, err := f.adapter.GetAccountSnapshot(ctx)
	require.NoError(t, err)
	require.NoError(t, f.store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpdateTradingAccountSnapshot(testSpace, testAccount, paperSnapshotRecordForTest(snapshot))
	}))
	beforeClose, err := f.store.GetTradingAccountByID(ctx, testAccount)
	require.NoError(t, err)
	require.NotEqual(t, "0", beforeClose.Snapshot.Balances[0].Locked)
	f.fake.mu.Lock()
	f.fake.quoteErr = errors.New("closing must not require public quotes")
	f.fake.mu.Unlock()

	require.NoError(t, (&papersimulation.Service{Store: f.store}).Close(ctx, testSpace, testAccount))
	closed, err := f.store.GetOrder(ctx, testSpace, string(placed.ID))
	require.NoError(t, err)
	require.Equal(t, "CANCELED", closed.State)
	require.Equal(t, "0", closed.RemainingReservedQuantity)
	account, err := f.store.GetTradingAccountByID(ctx, testAccount)
	require.NoError(t, err)
	require.Equal(t, "DISABLED", account.Status)
	require.False(t, account.Ready)
	require.Equal(t, beforeClose.Snapshot.ExchangeUpdatedAt, account.Snapshot.ExchangeUpdatedAt)
	require.Equal(t, beforeClose.LastSyncAt, account.LastSyncAt)
	require.Equal(t, beforeClose.SnapshotSourceTime, account.SnapshotSourceTime)
	require.Equal(t, "100000", account.Snapshot.AvailableFunds)
	require.Equal(t, []store.AssetBalance{{Asset: "USDT", Total: "100000", Available: "100000", Locked: "0"}}, account.Snapshot.Balances)
	logical, err := f.store.GetLogicalAccount(ctx, testSpace, "logical-close")
	require.NoError(t, err)
	require.Equal(t, "PAUSED", logical.AutomationState)

	f.close(t)
	restarted, err := store.Open(f.path)
	require.NoError(t, err)
	defer restarted.Close()
	candidates, err := restarted.ListPaperMatchCandidates(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, candidates)
	reloaded, err := restarted.GetTradingAccountByID(ctx, testAccount)
	require.NoError(t, err)
	require.Equal(t, "DISABLED", reloaded.Status)
	require.Equal(t, account.Snapshot, reloaded.Snapshot)
}

func TestPaperCancelThroughOrderServiceReleasesReservationE2E(t *testing.T) {
	ctx := context.Background()
	f := newProductionPaperFixture(t, exchange.MarketTypeSpot)
	placed := mustPlace(t, f, marketSpec("paper-cancel", exchange.SideBuy, "0.01"))
	open, err := f.orders.Submit(ctx, testSpace, string(placed.ID))
	require.NoError(t, err)
	require.Equal(t, orderdomain.Open, open.State)

	canceled, err := f.orders.Cancel(ctx, testSpace, string(open.ID))
	require.NoError(t, err)
	require.Equal(t, orderdomain.Canceled, canceled.State)
	record, err := f.store.GetOrder(ctx, testSpace, string(open.ID))
	require.NoError(t, err)
	require.Equal(t, "CANCELED", record.State)
	require.Equal(t, "0", record.RemainingReservedQuantity)
}

func TestClosePaperSimulationPreservesFilledBalancesWithoutQuotesE2E(t *testing.T) {
	for _, market := range []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap} {
		t.Run(string(market), func(t *testing.T) {
			ctx := context.Background()
			f := newProductionPaperFixture(t, market)
			spec := marketSpec("close-filled", exchange.SideBuy, "0.01")
			if market == exchange.MarketTypeSwap {
				spec = swapSpec("close-filled", exchange.SideBuy, "1", false)
			}
			placed := mustPlace(t, f, spec)
			_, err := f.orders.Submit(ctx, testSpace, string(placed.ID))
			require.NoError(t, err)
			productionPaperMatch(t, f)
			before, err := f.store.GetTradingAccountByID(ctx, testAccount)
			require.NoError(t, err)
			projection, err := f.store.GetPaperBalanceSnapshot(ctx, testSpace, testAccount)
			require.NoError(t, err)
			require.Equal(t, int64(1), projection.AppliedFillCount)
			f.fake.mu.Lock()
			f.fake.quoteErr = errors.New("public quotes unavailable")
			f.fake.mu.Unlock()
			service := &papersimulation.Service{Store: f.store}
			require.NoError(t, service.Close(ctx, testSpace, testAccount))
			require.NoError(t, service.Close(ctx, testSpace, testAccount))
			after, err := f.store.GetTradingAccountByID(ctx, testAccount)
			require.NoError(t, err)
			require.Equal(t, before.Snapshot, after.Snapshot)
			require.Equal(t, "DISABLED", after.Status)
			f.close(t)
			restarted, err := store.Open(f.path)
			require.NoError(t, err)
			defer restarted.Close()
			rebuilt, err := restarted.GetPaperBalanceSnapshot(ctx, testSpace, testAccount)
			require.NoError(t, err)
			require.Equal(t, projection, rebuilt)
			fillOrder, err := restarted.GetOrder(ctx, testSpace, string(placed.ID))
			require.NoError(t, err)
			require.Equal(t, "FILLED", fillOrder.State)
		})
	}
}

func TestClosePaperSimulationUsesCommittedPositionAfterRefreshFailureE2E(t *testing.T) {
	t.Run("open", func(t *testing.T) { testCloseAfterPositionRefreshFailure(t, false) })
	t.Run("reduce", func(t *testing.T) { testCloseAfterPositionRefreshFailure(t, true) })
}

func TestClosePaperSimulationUsesPositionMarkTimestampE2E(t *testing.T) {
	t.Run("known", func(t *testing.T) { testClosePositionMarkTimestamp(t, false) })
	t.Run("unknown", func(t *testing.T) { testClosePositionMarkTimestamp(t, true) })
}

func testClosePositionMarkTimestamp(t *testing.T, unknown bool) {
	t.Helper()
	ctx := context.Background()
	f := newProductionPaperFixture(t, exchange.MarketTypeSwap)
	placed := mustPlace(t, f, swapSpec("close-position-timestamp", exchange.SideBuy, "1", false))
	_, err := f.orders.Submit(ctx, testSpace, string(placed.ID))
	require.NoError(t, err)
	productionPaperMatch(t, f)
	positions, err := f.store.ListPositions(ctx, testSpace, testAccount, "")
	require.NoError(t, err)
	require.Len(t, positions, 1)
	if unknown {
		// Preserve the facts but emulate a legacy position lacking source time.
		positions[0].ExchangeUpdatedAt = 0
		require.NoError(t, f.store.Transaction(ctx, func(tx *store.Tx) error {
			if err := tx.ReplacePositionsForAccount(testSpace, testAccount, nil, testNow.Add(time.Hour).UnixMilli()); err != nil {
				return err
			}
			return tx.UpsertPosition(positions[0])
		}))
	}
	t2 := testNow.Add(time.Hour)
	adapter := f.adapter.(recordingAdapter).ExecutionAdapter.(*paperexec.Adapter)
	adapter.Now = func() time.Time { return t2 }
	f.fake.reference = exchange.ReferencePrice{Price: shared.MustDecimal("60000"), UpdatedAt: t2}
	fresh, err := adapter.GetAccountSnapshot(ctx)
	require.NoError(t, err)
	require.Equal(t, "6000", fresh.UsedMargin.String())
	require.NoError(t, f.store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpdateTradingAccountSnapshot(testSpace, testAccount, paperSnapshotRecordForTest(fresh))
	}))
	before, err := f.store.GetTradingAccountByID(ctx, testAccount)
	require.NoError(t, err)
	require.Equal(t, t2.UnixMilli(), before.Snapshot.ExchangeUpdatedAt)
	f.fake.quoteErr = errors.New("quotes unavailable at close")
	require.NoError(t, (&papersimulation.Service{Store: f.store}).Close(ctx, testSpace, testAccount))
	closed, err := f.store.GetTradingAccountByID(ctx, testAccount)
	require.NoError(t, err)
	require.Equal(t, "5000", closed.Snapshot.UsedMargin)
	require.Equal(t, "0", closed.Snapshot.UnrealizedPnL)
	require.Equal(t, positions[0].ExchangeUpdatedAt, closed.Snapshot.ExchangeUpdatedAt)
	require.Equal(t, before.LastSyncAt, closed.LastSyncAt)
	require.Equal(t, before.SnapshotSourceTime, closed.SnapshotSourceTime)
}

func testCloseAfterPositionRefreshFailure(t *testing.T, reduce bool) {
	t.Helper()
	ctx := context.Background()
	f := newProductionPaperFixture(t, exchange.MarketTypeSwap)
	side := exchange.SideBuy
	quantity := "1"
	wantMargin, wantAvailable := "5000", "95000"
	if reduce {
		initial := mustPlace(t, f, swapSpec("initial-position", exchange.SideBuy, "1", false))
		_, err := f.orders.Submit(ctx, testSpace, string(initial.ID))
		require.NoError(t, err)
		productionPaperMatch(t, f)
		side = exchange.SideSell
		quantity = "0.5"
		wantMargin, wantAvailable = "2500", "97500"
	}
	placed := mustPlace(t, f, swapSpec("close-after-refresh-failure", side, quantity, reduce))
	_, err := f.orders.Submit(ctx, testSpace, string(placed.ID))
	require.NoError(t, err)
	candidate, err := f.store.GetOrder(ctx, testSpace, string(placed.ID))
	require.NoError(t, err)
	before, err := f.store.GetTradingAccountByID(ctx, testAccount)
	require.NoError(t, err)
	f.fake.mu.Lock()
	f.fake.quoteErr = errors.New("public quotes unavailable after fill commit")
	f.fake.mu.Unlock()
	matcher := &paperexec.Matcher{
		Store: f.store, Reducer: f.reducer,
		Refresh: func(ctx context.Context, _ string) error {
			_, err := f.adapter.GetAccountSnapshot(ctx)
			return err
		},
	}
	err = matcher.MatchOrder(ctx, candidate, paperexec.Decision{Fill: exchange.Fill{
		ExchangeTradeID: "committed-before-refresh-failure", ExchangeOrderID: candidate.ExchangeOrderID,
		ClientOrderID: candidate.ClientOrderID, ExchangeSymbol: candidate.ExchangeSymbol,
		Side: side, PositionSide: exchange.PositionSideNet,
		Quantity: shared.MustDecimal(quantity), Price: shared.MustDecimal("50000"),
		TradedAt: testNow.Add(time.Second),
	}})
	require.ErrorContains(t, err, "public quotes unavailable")
	positions, err := f.store.ListPositions(ctx, testSpace, testAccount, "")
	require.NoError(t, err)
	require.Len(t, positions, 1)
	require.Equal(t, wantMargin, positions[0].UsedMargin)
	require.NoError(t, (&papersimulation.Service{Store: f.store}).Close(ctx, testSpace, testAccount))
	closed, err := f.store.GetTradingAccountByID(ctx, testAccount)
	require.NoError(t, err)
	require.Equal(t, wantMargin, closed.Snapshot.UsedMargin)
	require.Equal(t, "0", closed.Snapshot.UnrealizedPnL)
	require.Equal(t, wantAvailable, closed.Snapshot.AvailableFunds)
	require.Equal(t, before.Snapshot.ExchangeUpdatedAt, closed.Snapshot.ExchangeUpdatedAt)
	require.Equal(t, "DISABLED", closed.Status)
}

func TestPaperSnapshotFailsClosedWhenValuationQuoteErrorsE2E(t *testing.T) {
	for _, market := range []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap} {
		t.Run(string(market), func(t *testing.T) {
			f := newPaperFixture(t, market)
			spec := marketSpec("valuation-error", exchange.SideBuy, "0.01")
			if market == exchange.MarketTypeSwap {
				spec = swapSpec("valuation-error", exchange.SideBuy, "1", false)
			}
			order := mustPlace(t, f, spec)
			_, err := f.orders.Submit(context.Background(), testSpace, string(order.ID))
			require.NoError(t, err)
			f.fake.mu.Lock()
			f.fake.quoteErr = errors.New("public quote unavailable")
			f.fake.mu.Unlock()
			_, err = f.adapter.GetAccountSnapshot(context.Background())
			require.Error(t, err)
		})
	}
}
