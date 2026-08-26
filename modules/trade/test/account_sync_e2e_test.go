package test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

func TestAccountSyncFailureRecoveryImportsExternalOrderAndFill(t *testing.T) {
	ctx := context.Background()
	fake := newFakeExchange(exchange.MarketTypeSpot)
	f := newFixture(t, exchange.MarketTypeSpot, fake)

	fake.accountErr = errors.New("temporary Exchange snapshot failure")
	result, err := f.sync.SyncAccount(ctx, testAccount)
	require.Error(t, err)
	require.False(t, result.Ready)
	account, err := f.store.GetTradingAccountByID(ctx, testAccount)
	require.NoError(t, err)
	require.False(t, account.Ready)
	require.Contains(t, account.LastError, "snapshot failure")

	fake.accountErr = nil
	fake.orders["external-client"] = exchange.Order{
		ExchangeOrderID: "external-order", ClientOrderID: "external-client",
		ExchangeSymbol: testSymbol, OrderType: exchange.OrderTypeMarket, Side: exchange.SideBuy,
		Quantity: shared.MustDecimal("0.02"), FilledQuantity: shared.MustDecimal("0.01"),
		AveragePrice: shared.MustDecimal("50000"), Status: exchange.OrderStatusPartiallyFilled,
		CreatedAt: testNow.Add(time.Second), UpdatedAt: testNow.Add(2 * time.Second),
	}
	fake.fills = append(fake.fills, exchange.Fill{
		ExchangeTradeID: "external-trade", ExchangeOrderID: "external-order",
		ClientOrderID: "external-client", ExchangeSymbol: testSymbol, Side: exchange.SideBuy,
		Quantity: shared.MustDecimal("0.01"), Price: shared.MustDecimal("50000"),
		Fee: shared.MustDecimal("0.1"), FeeAsset: "USDT", SettlementAsset: "USDT",
		LiquidityRole: "TAKER", TradedAt: testNow.Add(3 * time.Second),
	})

	result, err = f.sync.SyncAccount(ctx, testAccount)
	require.NoError(t, err)
	require.True(t, result.Ready)
	require.Equal(t, 1, result.FillsIngested)
	account, err = f.store.GetTradingAccountByID(ctx, testAccount)
	require.NoError(t, err)
	require.True(t, account.Ready)
	require.Empty(t, account.LastError)

	order, err := f.store.GetOrderByClientID(ctx, testSpace, testAccount, "external-client")
	require.NoError(t, err)
	require.Equal(t, "EXTERNAL", order.OwnerType)
	require.NotEmpty(t, order.OwnerID)
	require.Equal(t, "PARTIALLY_FILLED", order.State)
	require.Equal(t, "0.01", order.FilledQuantity)
	fills, total, err := f.store.ListFills(
		ctx,
		testSpace,
		store.FillQuery{TradingAccountID: testAccount, Limit: 10},
	)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Equal(t, "external-trade", fills[0].ExchangeTradeID)
}

func TestAccountSyncEachSnapshotFailureKeepsNotReadyAndRecovers(t *testing.T) {
	failures := []struct {
		name string
		set  func(*fakeExchange, error)
	}{
		{name: "open orders", set: func(f *fakeExchange, err error) { f.openOrdersErr = err }},
		{name: "positions", set: func(f *fakeExchange, err error) { f.positionsErr = err }},
		{name: "account", set: func(f *fakeExchange, err error) { f.accountErr = err }},
		{name: "recent fills", set: func(f *fakeExchange, err error) { f.fillsErr = err }},
	}
	for _, failure := range failures {
		t.Run(failure.name, func(t *testing.T) {
			fake := newFakeExchange(exchange.MarketTypeSpot)
			f := newFixture(t, exchange.MarketTypeSpot, fake)
			failure.set(fake, errors.New("injected "+failure.name+" failure"))

			result, err := f.sync.SyncAccount(context.Background(), testAccount)
			require.Error(t, err)
			require.False(t, result.Ready)
			account, getErr := f.store.GetTradingAccountByID(context.Background(), testAccount)
			require.NoError(t, getErr)
			require.False(t, account.Ready)

			failure.set(fake, nil)
			result, err = f.sync.SyncAccount(context.Background(), testAccount)
			require.NoError(t, err)
			require.True(t, result.Ready)
			account, getErr = f.store.GetTradingAccountByID(context.Background(), testAccount)
			require.NoError(t, getErr)
			require.True(t, account.Ready)
		})
	}
}
