package test

import (
	"context"
	"testing"

	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

func TestSwapNetCrossLeverageReduceOnlyAndRealizedPnL(t *testing.T) {
	ctx := context.Background()
	fake := newFakeExchange(exchange.MarketTypeSwap)
	f := newFixture(t, exchange.MarketTypeSwap, fake)
	require.NoError(t, f.adapter.SetMarginMode(ctx, testSymbol, exchange.MarginModeCross))
	require.NoError(t, f.adapter.SetLeverage(ctx, testSymbol, shared.MustDecimal("10")))

	openLong := mustPlace(t, f, swapSpec("long-open", exchange.SideBuy, "0.01", false))
	openLong, err := f.orders.Submit(ctx, testSpace, string(openLong.ID))
	require.NoError(t, err)
	require.Equal(t, orderdomain.Open, openLong.State)
	applyFakeFill(t, f, "long-open", "long-open-fill", "0.01", "50000", "0")
	assertNetPosition(t, f.store, "0.01", "10")

	partial := swapSpec("long-reduce", exchange.SideSell, "0.004", true)
	partial.ReferencePrice = shared.MustDecimal("51000")
	partialOrder := mustPlace(t, f, partial)
	partialOrder, err = f.orders.Submit(ctx, testSpace, string(partialOrder.ID))
	require.NoError(t, err)
	require.Equal(t, orderdomain.Open, partialOrder.State)
	applyFakeFill(t, f, "long-reduce", "long-reduce-fill", "0.004", "51000", "4")
	assertNetPosition(t, f.store, "0.006", "10")
	partialRealized := realizedPnL(t, f.store, 2)
	require.Equal(t, "4", partialRealized.String())

	closeLong := swapSpec("long-close", exchange.SideSell, "0.006", true)
	closeLong.ReferencePrice = shared.MustDecimal("51000")
	closeOrder := mustPlace(t, f, closeLong)
	_, err = f.orders.Submit(ctx, testSpace, string(closeOrder.ID))
	require.NoError(t, err)
	applyFakeFill(t, f, "long-close", "long-close-fill", "0.006", "51000", "6")
	positions, err := f.store.ListPositions(ctx, testSpace, testAccount, testSymbol)
	require.NoError(t, err)
	require.Len(t, positions, 1)
	require.Equal(t, "0", positions[0].SignedQuantity)

	longRealized := realizedPnL(t, f.store, 3)
	require.Equal(t, "10", longRealized.String())
	account, err := f.store.GetExchangeAccountByID(ctx, testAccount)
	require.NoError(t, err)
	require.Equal(t, string(exchange.MarginModeCross), account.MarginMode)
	require.Equal(t, "10", account.LeverageSettings[testSymbol])
	fake.mu.Lock()
	requests := append([]exchange.OrderRequest(nil), fake.requests...)
	leverageCalls := append([]leverageCall(nil), fake.leverageCalls...)
	marginCalls := append([]marginCall(nil), fake.marginCalls...)
	fake.mu.Unlock()
	require.Len(t, requests, 3)
	require.Equal(t, []exchange.Side{exchange.SideBuy, exchange.SideSell, exchange.SideSell}, []exchange.Side{
		requests[0].Side, requests[1].Side, requests[2].Side,
	})
	require.Equal(t, []bool{false, true, true}, []bool{
		requests[0].ReduceOnly, requests[1].ReduceOnly, requests[2].ReduceOnly,
	})
	for index, quantity := range []string{"0.01", "0.004", "0.006"} {
		require.Equal(t, exchange.OrderTypeMarket, requests[index].OrderType)
		require.Nil(t, requests[index].LimitPrice)
		require.Equal(t, exchange.PositionSideNet, requests[index].PositionSide)
		require.Equal(t, quantity, requests[index].Quantity.String(), "domain quantity remains base quantity")
	}
	require.Len(t, leverageCalls, 1)
	require.Equal(t, testSymbol, leverageCalls[0].symbol)
	require.Equal(t, "10", leverageCalls[0].leverage.String())
	require.Equal(t, []marginCall{{symbol: testSymbol, mode: exchange.MarginModeCross}}, marginCalls)

	shortFake := newFakeExchange(exchange.MarketTypeSwap)
	shortFixture := newFixture(t, exchange.MarketTypeSwap, shortFake)
	openShort := swapSpec("short-open", exchange.SideSell, "0.01", false)
	openShort.ReferencePrice = shared.MustDecimal("51000")
	shortOrder := mustPlace(t, shortFixture, openShort)
	_, err = shortFixture.orders.Submit(ctx, testSpace, string(shortOrder.ID))
	require.NoError(t, err)
	applyFakeFill(t, shortFixture, "short-open", "short-open-fill", "0.01", "51000", "0")
	assertNetPosition(t, shortFixture.store, "-0.01", "10")

	closeShort := swapSpec("short-close", exchange.SideBuy, "0.01", true)
	closeShort.ReferencePrice = shared.MustDecimal("50000")
	closeShortOrder := mustPlace(t, shortFixture, closeShort)
	_, err = shortFixture.orders.Submit(ctx, testSpace, string(closeShortOrder.ID))
	require.NoError(t, err)
	applyFakeFill(t, shortFixture, "short-close", "short-close-fill", "0.01", "50000", "10")
	shortRealized := realizedPnL(t, shortFixture.store, 2)
	require.Equal(t, "10", shortRealized.String())
}

func applyFakeFill(t *testing.T, f *fixture, clientID, tradeID, quantity, price, realized string) {
	t.Helper()
	fill := f.fake.emitFill(clientID, tradeID, quantity, price, realized)
	_, err := f.sync.ApplyFill(context.Background(), testAccount, fill)
	require.NoError(t, err)
}

func realizedPnL(t *testing.T, tradeStore *store.Store, expected int64) shared.Decimal {
	t.Helper()
	fills, total, err := tradeStore.ListFills(
		context.Background(),
		testSpace,
		store.FillQuery{ExchangeAccountID: testAccount, Limit: 10},
	)
	require.NoError(t, err)
	require.Equal(t, expected, total)
	realized := shared.Zero()
	for _, fill := range fills {
		require.Equal(t, string(exchange.PositionSideNet), fill.PositionSide)
		realized = realized.Add(shared.MustDecimal(fill.RealizedPnL))
	}
	return realized
}

func assertNetPosition(t *testing.T, tradeStore *store.Store, quantity, leverage string) {
	t.Helper()
	position, found, err := tradeStore.GetPosition(
		context.Background(),
		testSpace,
		testAccount,
		testSymbol,
		string(exchange.PositionSideNet),
	)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, quantity, position.SignedQuantity)
	require.Equal(t, leverage, position.Leverage)
	require.Equal(t, string(exchange.MarginModeCross), position.MarginMode)
	require.Equal(t, "0", position.LiquidationPrice, "tests must not invent an Exchange liquidation price")
}
