package test

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/operator"
	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	paperexec "github.com/mooyang-code/moox/modules/trade/internal/execution/paper"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

func TestPaperPendingReservationsCountOnceAfterSnapshotE2E(t *testing.T) {
	for _, market := range []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap} {
		t.Run(string(market), func(t *testing.T) {
			ctx := context.Background()
			f := newProductionPaperFixture(t, market)
			firstSpec := marketSpec("reserve-first", exchange.SideBuy, "1")
			secondSpec := marketSpec("reserve-second", exchange.SideBuy, "0.6")
			thirdSpec := marketSpec("reserve-over-budget", exchange.SideBuy, "0.5")
			if market == exchange.MarketTypeSwap {
				firstSpec = swapSpec("reserve-first", exchange.SideBuy, "10", false)
				secondSpec = swapSpec("reserve-second", exchange.SideBuy, "6", false)
				thirdSpec = swapSpec("reserve-over-budget", exchange.SideBuy, "5", false)
			}
			first := mustPlace(t, f, firstSpec)
			_, err := f.sync.SyncAccount(ctx, testAccount)
			require.NoError(t, err)
			account, err := f.store.GetTradingAccountByID(ctx, testAccount)
			require.NoError(t, err)
			require.Equal(t, "50000", account.Snapshot.AvailableFunds)

			second, err := f.orders.Place(ctx, testSpace, secondSpec)
			require.NoError(t, err, "the snapshot already contains the first reservation")
			require.Equal(t, orderdomain.Pending, second.State)
			_, err = f.orders.Place(ctx, testSpace, thirdSpec)
			require.ErrorIs(t, err, orderapp.ErrInsufficientFunds)

			_, err = f.sync.SyncAccount(ctx, testAccount)
			require.NoError(t, err)
			first, err = f.orders.Submit(ctx, testSpace, string(first.ID))
			require.NoError(t, err, "submitting a reserved order must not reserve it twice")
			require.Equal(t, orderdomain.Open, first.State)
		})
	}
}

func TestPaperManualRefreshReplacesReflectedReservationE2E(t *testing.T) {
	for _, factor := range []string{"1.05", "0.5", "1.2"} {
		t.Run(factor, func(t *testing.T) {
			ctx := context.Background()
			f := newProductionPaperFixture(t, exchange.MarketTypeSwap)
			seedSwapLogicalAccount(t, f.store)
			now := testNow
			f.orders.Now = func() time.Time { return now }
			f.orders.Validator.Now = func() time.Time { return now }
			f.orders.Validator.MaxChildNotional = shared.MustDecimal("2000000")
			f.adapter.(recordingAdapter).ExecutionAdapter.(*paperexec.Adapter).Now = func() time.Time { return now }
			service := &operator.Service{Store: f.store, Orders: manualSubmitDispatchLoss{Service: f.orders},
				Syncer: syncBridge{service: f.sync}, Prices: logicalAccountE2EPriceSource{at: testNow},
				Now: func() time.Time { return now }, ManualSubmitWindow: 2 * time.Minute}
			command := operator.ManualOrderCommand{SpaceID: testSpace, ActionID: "reflected-refresh", TradingAccountID: testAccount,
				ClientOrderID: "reflected-refresh", InstrumentID: testInstrumentID, Type: exchange.OrderTypeMarket,
				Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet, Quantity: shared.MustDecimal("18"), Reason: "delayed paper order"}
			initial, err := service.PlaceManualOrder(ctx, command)
			require.NoError(t, err)
			_, err = f.sync.SyncAccount(ctx, testAccount)
			require.NoError(t, err)
			now = now.Add(70 * time.Second)
			f.fake.reference.Price = f.fake.reference.Price.Mul(shared.MustDecimal(factor))
			f.fake.reference.UpdatedAt = now
			service.Orders = f.orders
			result, err := service.PlaceManualOrder(ctx, command)
			require.NoError(t, err)
			require.Equal(t, initial.Order.OrderID, result.Order.OrderID)
			if factor == "1.2" {
				require.Equal(t, "RUNNING", result.Action.Status)
				require.Contains(t, result.Action.LastError, "insufficient")
				require.Equal(t, initial.Order.RemainingReservedQuantity, result.Order.RemainingReservedQuantity)
				require.Equal(t, initial.Order.ReferencePrice, result.Order.ReferencePrice)
				require.Zero(t, f.fake.placeCalls)
				return
			}
			require.Equal(t, "COMPLETED", result.Action.Status)
			require.Equal(t, shared.MustDecimal("90000").Mul(shared.MustDecimal(factor)).String(), result.Order.RemainingReservedQuantity)
			require.Equal(t, 1, f.fake.placeCalls)
		})
	}
}

type paperSnapshotQueryCounter struct {
	*store.Store
	fills, orders, balances int
}

func (s *paperSnapshotQueryCounter) ListFills(ctx context.Context, space string, query store.FillQuery) ([]store.FillRecord, int64, error) {
	s.fills++
	return s.Store.ListFills(ctx, space, query)
}

func (s *paperSnapshotQueryCounter) ListOrdersForAccount(ctx context.Context, space, account string, since int64) ([]store.OrderRecord, error) {
	s.orders++
	return s.Store.ListOrdersForAccount(ctx, space, account, since)
}

func (s *paperSnapshotQueryCounter) GetPaperBalanceSnapshot(ctx context.Context, space, account string) (store.PaperBalanceSnapshot, error) {
	s.balances++
	return s.Store.GetPaperBalanceSnapshot(ctx, space, account)
}

func TestPaperSnapshotReadsProjectionInsteadOfHistoryE2E(t *testing.T) {
	f := newProductionPaperFixture(t, exchange.MarketTypeSpot)
	for i := 0; i < 3; i++ {
		spec := marketSpec("", exchange.SideBuy, "0.01")
		order := mustPlace(t, f, spec)
		_, err := f.orders.Submit(context.Background(), testSpace, string(order.ID))
		require.NoError(t, err)
		productionPaperMatch(t, f)
	}
	counter := &paperSnapshotQueryCounter{Store: f.store}
	adapter := f.adapter.(recordingAdapter).ExecutionAdapter.(*paperexec.Adapter)
	adapter.Store = counter
	for i := 0; i < 2; i++ {
		snapshot, err := adapter.GetAccountSnapshot(context.Background())
		require.NoError(t, err)
		require.Equal(t, "98500", snapshot.AvailableFunds.String())
	}
	require.Equal(t, 2, counter.balances)
	require.Zero(t, counter.fills, "snapshot must not replay fill history")
	require.Zero(t, counter.orders, "snapshot must not scan terminal order history")
}

func TestPaperRefreshFundsCoverFrozenExecutionPriceE2E(t *testing.T) {
	ctx := context.Background()
	f := newProductionPaperFixture(t, exchange.MarketTypeSwap)
	seedSwapLogicalAccount(t, f.store)
	now := testNow
	f.orders.Now = func() time.Time { return now }
	f.orders.Validator.Now = func() time.Time { return now }
	f.adapter.(recordingAdapter).ExecutionAdapter.(*paperexec.Adapter).Now = func() time.Time { return now }
	require.NoError(t, f.store.DBForTest().Exec("UPDATE t_paper_account_configs SET c_slippage_bps = '100' WHERE c_trading_account_id = ?", testAccount).Error)
	spec := swapSpec("frozen-price-reserve", exchange.SideBuy, "19", false)
	spec.Owner = orderdomain.OrderOwner{Type: orderdomain.OwnerOperator, OwnerID: "frozen-price-action", LogicalAccountID: testLogicalAccount}
	order := mustPlace(t, f, spec)
	before, err := f.store.GetOrder(ctx, testSpace, string(order.ID))
	require.NoError(t, err)
	require.Equal(t, "95950", before.RemainingReservedQuantity)
	_, err = f.sync.SyncAccount(ctx, testAccount)
	require.NoError(t, err)
	now = now.Add(70 * time.Second)
	f.fake.reference.Price = shared.MustDecimal("52500")
	f.fake.reference.UpdatedAt = now
	require.NoError(t, f.store.DBForTest().Exec("UPDATE t_paper_account_configs SET c_slippage_bps = '0' WHERE c_trading_account_id = ?", testAccount).Error)
	_, err = f.orders.Submit(ctx, testSpace, string(order.ID))
	require.ErrorIs(t, err, orderapp.ErrInsufficientFunds, "the frozen 1%% slippage still costs 100747.5, not 99750")
	after, err := f.store.GetOrder(ctx, testSpace, string(order.ID))
	require.NoError(t, err)
	require.Equal(t, before.ReferencePrice, after.ReferencePrice)
	require.Equal(t, before.PaperExecutionPrice, after.PaperExecutionPrice)
	require.Equal(t, before.RemainingReservedQuantity, after.RemainingReservedQuantity)
	require.Equal(t, "PENDING", after.State)
	require.Zero(t, f.fake.placeCalls)
}

func TestPaperFundsRejectInvalidOpenPositionMarginE2E(t *testing.T) {
	ctx := context.Background()
	f := newProductionPaperFixture(t, exchange.MarketTypeSwap)
	first := mustPlace(t, f, swapSpec("invalid-margin-first", exchange.SideBuy, "1", false))
	_, err := f.orders.Submit(ctx, testSpace, string(first.ID))
	require.NoError(t, err)
	productionPaperMatch(t, f)
	require.NoError(t, f.store.DBForTest().Exec("UPDATE t_trading_positions SET c_leverage = '0' WHERE c_trading_account_id = ?", testAccount).Error)
	_, err = f.orders.Place(ctx, testSpace, swapSpec("invalid-margin-next", exchange.SideBuy, "0.01", false))
	require.ErrorIs(t, err, store.ErrInvalidRecord, "an open position with invalid leverage cannot mean zero used margin")
}

func TestPaperFundsUseCashAndCurrentMarginNotStaleSnapshotE2E(t *testing.T) {
	for _, market := range []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap} {
		t.Run(string(market), func(t *testing.T) {
			ctx := context.Background()
			f := newProductionPaperFixture(t, market)
			firstSpec := marketSpec("cash-first", exchange.SideBuy, "1")
			nextSpec := marketSpec("cash-next", exchange.SideBuy, "1.2")
			if market == exchange.MarketTypeSwap {
				firstSpec = swapSpec("cash-first", exchange.SideBuy, "10", false)
				nextSpec = swapSpec("cash-next", exchange.SideBuy, "12", false)
			}
			first := mustPlace(t, f, firstSpec)
			_, err := f.orders.Submit(ctx, testSpace, string(first.ID))
			require.NoError(t, err)
			productionPaperMatch(t, f)
			// A stale display snapshot must not restore already spent cash or margin.
			account, err := f.store.GetTradingAccountByID(ctx, testAccount)
			require.NoError(t, err)
			account.Snapshot.AvailableFunds = "100000"
			for i := range account.Snapshot.Balances {
				if account.Snapshot.Balances[i].Asset == "USDT" {
					account.Snapshot.Balances[i].Available = "100000"
				}
			}
			setSpotSnapshot(t, f, account.Snapshot)
			_, err = f.orders.Place(ctx, testSpace, nextSpec)
			require.ErrorIs(t, err, orderapp.ErrInsufficientFunds)
		})
	}
}
