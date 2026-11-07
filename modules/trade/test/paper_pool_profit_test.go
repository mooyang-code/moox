package test

import (
	"context"
	"testing"

	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

func newProfitPoolFixture(t *testing.T) *fixture {
	t.Helper()
	f := newProductionPaperFixture(t, exchange.MarketTypeSwap)
	require.NoError(t, f.store.DBForTest().Exec("UPDATE t_trading_accounts SET c_markets = 'SPOT,SWAP' WHERE c_trading_account_id = ?", testAccount).Error)
	spot := newFakeExchange(exchange.MarketTypeSpot).instrument
	require.NoError(t, f.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: string(spot.Exchange), MarketType: "SPOT", Environment: string(exchange.AccountEnvironmentProduction),
			ExchangeSymbol: spot.ExchangeSymbol, InstrumentID: spot.InstrumentID, BaseAsset: "BTC", QuoteAsset: "USDT",
			ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001", PriceTick: "0.1", MinNotional: "1",
			Status: "TRADING", ExchangeUpdatedAt: testNow.UnixMilli(),
		})
	}))
	return f
}

func usdtCash(t *testing.T, f *fixture) shared.Decimal {
	t.Helper()
	balances, err := f.store.GetPaperBalanceSnapshot(context.Background(), testSpace, testAccount)
	require.NoError(t, err)
	return balances.Totals["USDT"]
}

// 合约浮盈为正时，浮盈不能拿去买现货（现货不借币）：现货买单容量最多是“现金 - 保证金”，买完结算币现金不为负。
func TestPaperSpotBuyCannotSpendSwapUnrealizedProfit(t *testing.T) {
	ctx := context.Background()
	f := newProfitPoolFixture(t)
	first := mustPlace(t, f, swapSpec("swap-open", exchange.SideBuy, "1", false))
	_, err := f.orders.Submit(ctx, testSpace, string(first.ID))
	require.NoError(t, err)
	productionPaperMatch(t, f)
	cash := usdtCash(t, f)

	// 价格涨到 70000：合约浮盈，资金池（给合约用）变大，但现货买单只能用现金扣掉保证金。
	f.fake.mu.Lock()
	f.fake.reference = exchange.ReferencePrice{Price: shared.MustDecimal("70000"), UpdatedAt: testNow}
	f.fake.mu.Unlock()
	snapshot, err := f.adapter.GetAccountSnapshot(ctx)
	require.NoError(t, err)
	require.True(t, snapshot.UnrealizedPnL.Cmp(shared.Zero()) > 0)
	spotAvailable := cash.Sub(snapshot.UsedMargin)
	for _, balance := range snapshot.Balances {
		if balance.Asset == "USDT" {
			require.Zero(t, balance.Available.Cmp(spotAvailable), "现货买单看的可用余额不含浮盈：%s", balance.Available)
		}
	}
	require.True(t, snapshot.AvailableFunds.Cmp(spotAvailable) > 0, "合约方向的可用资金仍含浮盈")

	spec := marketSpec("spot-buy-cap", exchange.SideBuy, "100")
	spec.ReferencePrice = shared.MustDecimal("70000")
	capacity, err := f.orders.Capacity(ctx, testSpace, spec)
	require.NoError(t, err)
	maxQuantity := spotAvailable.Div(shared.MustDecimal("70000"))
	require.True(t, capacity.Cmp(maxQuantity) <= 0, "容量 %s 不能超过 %s", capacity, maxQuantity)

	tooBig := marketSpec("spot-buy-big", exchange.SideBuy, maxQuantity.RoundDown(3).Add(shared.MustDecimal("0.1")).String())
	tooBig.ReferencePrice = shared.MustDecimal("70000")
	_, err = f.orders.Place(ctx, testSpace, tooBig)
	require.ErrorIs(t, err, orderapp.ErrInsufficientFunds)

	// 容量内的现货买单下得了，撮合后现金不为负。
	withinCapacity := marketSpec("spot-buy-ok", exchange.SideBuy, capacity.RoundDown(3).String())
	withinCapacity.ReferencePrice = shared.MustDecimal("70000")
	bought := mustPlace(t, f, withinCapacity)
	_, err = f.orders.Submit(ctx, testSpace, string(bought.ID))
	require.NoError(t, err)
	productionPaperMatch(t, f)
	require.False(t, usdtCash(t, f).IsNegative(), "买完现货结算币现金不能为负")
}
