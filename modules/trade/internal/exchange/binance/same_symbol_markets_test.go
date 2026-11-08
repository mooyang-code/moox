package binance

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// 官方样例的现货 executionReport 和合约 ORDER_TRADE_UPDATE 代码不同（ETHBTC、BTCUSDT）。
// 这里把现货样例的代码、客户端订单号、交易所订单号、成交号改成与合约样例完全相同，模拟币安两个市场撞号的情形，
// 两条事件各由自己市场的适配器解析，写进同一个双市场账户后必须各记各的。
func TestSameNamedSpotAndSwapEventsDoNotCollideInOneAccount(t *testing.T) {
	spotRaw := string(sample(t, "spot_execution_report_trade.json"))
	for from, to := range map[string]string{
		`"s":"ETHBTC"`: `"s":"BTCUSDT"`, `"c":"mUvoqJxFIILMdfAW5iGSOW"`: `"c":"TEST"`,
		`"i":4293153`: `"i":8886774`, `"t":5001`: `"t":123456`, `"N":"ETH"`: `"N":"USDT"`,
	} {
		require.Contains(t, spotRaw, from)
		spotRaw = strings.Replace(spotRaw, from, to, 1)
	}
	spot := &recordingHandler{}
	require.NoError(t, testAdapter(exchange.MarketTypeSpot, "http://unused").dispatchPrivate(context.Background(), []byte(spotRaw), spot))
	swap, err := dispatchSample(t, exchange.MarketTypeSwap, "futures_order_trade_update.json")
	require.NoError(t, err)
	require.Len(t, spot.orders, 1)
	require.Len(t, spot.fills, 1)
	require.Len(t, swap.orders, 1)
	require.Len(t, swap.fills, 1)

	// 两边的标识完全相同，只有市场不同。
	require.Equal(t, spot.orders[0].ExchangeSymbol, swap.orders[0].ExchangeSymbol)
	require.Equal(t, spot.orders[0].ExchangeOrderID, swap.orders[0].ExchangeOrderID)
	require.Equal(t, spot.orders[0].ClientOrderID, swap.orders[0].ClientOrderID)
	require.Equal(t, spot.fills[0].ExchangeTradeID, swap.fills[0].ExchangeTradeID)
	require.Equal(t, exchange.MarketTypeSpot, spot.fills[0].Market)
	require.Equal(t, exchange.MarketTypeSwap, swap.fills[0].Market)

	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tradeStore.Close()) })
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "account-1", Name: "dual", Exchange: "BINANCE", Markets: []string{"SPOT", "SWAP"},
			ExecutionMode: "PAPER", Environment: "PAPER", SettlementAsset: "USDT", MarginMode: "CROSS",
			LeverageSettings: store.LeverageSettings{store.DefaultLeverageKey: "10"}, Status: "ENABLED",
		}); err != nil {
			return err
		}
		for _, order := range []exchange.Order{spot.orders[0], swap.orders[0]} {
			market := string(order.Market)
			if err := tx.UpsertInstrument(store.InstrumentRecord{
				Exchange: "BINANCE", MarketType: market, ExchangeSymbol: "BTCUSDT", InstrumentID: "BTC-USDT-" + market,
				BaseAsset: "BTC", QuoteAsset: "USDT", SettlementAsset: "USDT", Linear: market == "SWAP",
				ContractValue: "1", ContractValueAsset: "BTC", ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001",
				PriceTick: "0.1", Status: "TRADING",
			}); err != nil {
				return err
			}
			positionSide := ""
			if market == "SWAP" {
				positionSide = string(exchange.PositionSideNet)
			}
			if err := tx.CreateOrder(store.OrderRecord{
				SpaceID: "space-1", OrderID: "order-" + market, TradingAccountID: "account-1",
				ClientOrderID: order.ClientOrderID, ExchangeOrderID: order.ExchangeOrderID,
				Exchange: "BINANCE", MarketType: market, InstrumentID: "BTC-USDT-" + market, ExchangeSymbol: order.ExchangeSymbol,
				OrderType: string(exchange.OrderTypeMarket), Side: string(exchange.SideBuy), PositionSide: positionSide,
				Quantity: order.Quantity.String(), ReferencePrice: "100", OwnerType: "EXTERNAL", OwnerID: order.ExchangeOrderID,
				State: "OPEN", Version: 1,
			}); err != nil {
				return err
			}
		}
		return nil
	}))
	reducer := &consumer.Reducer{Store: tradeStore}
	for _, fill := range []exchange.Fill{spot.fills[0], swap.fills[0]} {
		applied, err := reducer.ApplyFill(context.Background(), fill, consumer.Source{SpaceID: "space-1", TradingAccountID: "account-1", Kind: consumer.OriginRESTSnapshot})
		require.NoError(t, err)
		require.True(t, applied, "同号成交在另一个市场里不算重复：%s", fill.Market)
	}
	for _, market := range []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap} {
		fills, _, err := tradeStore.ListFills(context.Background(), "space-1", store.FillQuery{TradingAccountID: "account-1", Market: string(market), ExchangeSymbol: "BTCUSDT", Limit: 10})
		require.NoError(t, err)
		require.Len(t, fills, 1, string(market))
		require.Equal(t, "order-"+string(market), fills[0].OrderID, "成交挂到自己市场的订单上")
	}
}
