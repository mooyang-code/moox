package test

import (
	"context"
	"errors"
	"testing"

	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/paper"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

func TestSpotRejectsOffStepBaseQuantityBeforeExchangePlacement(t *testing.T) {
	fake := newFakeExchange(exchange.MarketTypeSpot)
	f := newFixture(t, exchange.MarketTypeSpot, fake)
	_, err := f.orders.Place(
		context.Background(),
		testSpace,
		marketSpec("spot-off-step", exchange.SideBuy, "0.0105"),
	)
	require.Error(t, err)
	require.True(t, errors.Is(err, orderapp.ErrQuantityRule))
	fake.mu.Lock()
	require.Empty(t, fake.requests)
	fake.mu.Unlock()
}

func TestSpotPersistsNormalizedNonzeroExchangeFee(t *testing.T) {
	ctx := context.Background()
	fake := newFakeExchange(exchange.MarketTypeSpot)
	f := newFixture(t, exchange.MarketTypeSpot, fake)
	order := mustPlace(t, f, marketSpec("spot-fee", exchange.SideBuy, "0.01"))
	_, err := f.orders.Submit(ctx, testSpace, string(order.ID))
	require.NoError(t, err)
	fill := fake.emitFill("spot-fee", "spot-fee-fill", "0.01", "50000", "0")
	applied, err := f.sync.ApplyFill(ctx, testAccount, fill)
	require.NoError(t, err)
	require.True(t, applied)

	fills, total, err := f.store.ListFills(
		ctx,
		testSpace,
		store.FillQuery{ExchangeAccountID: testAccount, Limit: 10},
	)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Equal(t, "0.1", fills[0].Fee)
	require.Equal(t, "USDT", fills[0].FeeAsset)
}

func TestSpotPaperMarketBuySellPersistsAndRestarts(t *testing.T) {
	ctx := context.Background()
	f := newPaperFixture(t, exchange.MarketTypeSpot)

	buy := mustPlace(t, f, marketSpec("spot-buy", exchange.SideBuy, "0.01"))
	buy, err := f.orders.Submit(ctx, testSpace, string(buy.ID))
	require.NoError(t, err)
	require.Equal(t, orderdomain.Filled, buy.State)

	sell := mustPlace(t, f, marketSpec("spot-sell", exchange.SideSell, "0.01"))
	sell, err = f.orders.Submit(ctx, testSpace, string(sell.ID))
	require.NoError(t, err)
	require.Equal(t, orderdomain.Filled, sell.State)

	orders, total, err := f.store.ListOrders(ctx, testSpace, store.OrderQuery{
		ExchangeAccountID: testAccount, Limit: 10,
	})
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
	require.Len(t, orders, 2)
	for _, current := range orders {
		require.Equal(t, string(orderdomain.Filled), current.State)
		require.Empty(t, current.PositionSide)
		require.NotEmpty(t, current.ExchangeOrderID)
	}
	fills, fillTotal, err := f.store.ListFills(ctx, testSpace, store.FillQuery{
		ExchangeAccountID: testAccount, Limit: 10,
	})
	require.NoError(t, err)
	require.Equal(t, int64(2), fillTotal)
	for _, fill := range fills {
		require.Empty(t, fill.PositionSide)
		require.Equal(t, "0", fill.Fee)
	}
	account, err := f.store.GetExchangeAccountByID(ctx, testAccount)
	require.NoError(t, err)
	require.True(t, account.Ready)
	require.Equal(t, "100000", account.Snapshot.AvailableFunds)
	f.fake.mu.Lock()
	requests := append([]exchange.OrderRequest(nil), f.fake.requests...)
	f.fake.mu.Unlock()
	require.Len(t, requests, 2)
	require.Equal(t, exchange.OrderTypeMarket, requests[0].OrderType)
	require.Nil(t, requests[0].LimitPrice)
	require.Equal(t, exchange.SideBuy, requests[0].Side)
	require.Equal(t, exchange.SideSell, requests[1].Side)
	for _, request := range requests {
		require.Equal(t, testSymbol, request.Symbol)
		require.Equal(t, exchange.PositionSideUnspecified, request.PositionSide)
		require.False(t, request.ReduceOnly)
		require.Equal(t, "0.01", request.Quantity.String())
	}

	path := f.path
	f.close(t)
	restarted, err := store.Open(path)
	require.NoError(t, err)
	defer restarted.Close()
	recoveredOrders, recoveredTotal, err := restarted.ListOrders(
		ctx,
		testSpace,
		store.OrderQuery{ExchangeAccountID: testAccount, Limit: 10},
	)
	require.NoError(t, err)
	require.Equal(t, int64(2), recoveredTotal)
	require.Len(t, recoveredOrders, 2)
	recoveredFills, recoveredFillTotal, err := restarted.ListFills(
		ctx,
		testSpace,
		store.FillQuery{ExchangeAccountID: testAccount, Limit: 10},
	)
	require.NoError(t, err)
	require.Equal(t, int64(2), recoveredFillTotal)
	require.Len(t, recoveredFills, 2)

	base := newFakeExchange(exchange.MarketTypeSpot)
	recoveredPaper := paper.New(
		base, restarted, testSpace, testAccount, exchange.MarketTypeSpot, "USDT",
		shared.MustDecimal("100000"), exchange.MarginModeUnspecified, nil,
	)
	_, err = recoveredPaper.LoadInstruments(ctx)
	require.NoError(t, err)
	snapshot, err := recoveredPaper.GetAccountSnapshot(ctx)
	require.NoError(t, err)
	require.Equal(t, "100000", snapshot.AvailableFunds.String())
	require.Equal(t, "0", assetAvailable(snapshot, "BTC"))
}

func assetAvailable(snapshot exchange.AccountSnapshot, asset string) string {
	for _, balance := range snapshot.Balances {
		if balance.Asset == asset {
			return balance.Available.String()
		}
	}
	return "0"
}
