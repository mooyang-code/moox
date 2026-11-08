package accountsync

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// 同一个双市场实盘账户：币安现货和合约的交易所代码同名（BTCUSDT），客户端订单号、交易所订单号也可能相同。
func newDualMarketSync(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	tradeStore := openSyncStore(t)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			CreatedAt: time.UnixMilli(1), SpaceID: "space-1", TradingAccountID: "account-1", Name: "dual",
			Exchange: "BINANCE", Markets: []string{"SPOT", "SWAP"}, ExecutionMode: "LIVE", Environment: "PRODUCTION",
			AccountMode:        string(exchange.AccountModeBinanceStandard),
			CredentialSecretID: "secret-1", SettlementAsset: "USDT", MarginMode: "CROSS",
			LeverageSettings: store.LeverageSettings{store.DefaultLeverageKey: "5"},
			Status:           "ENABLED", Ready: true,
		}); err != nil {
			return err
		}
		for _, market := range []string{"SPOT", "SWAP"} {
			record := store.InstrumentRecord{
				Exchange: "BINANCE", MarketType: market, ExchangeSymbol: "BTCUSDT", InstrumentID: "BTC-USDT-" + market,
				BaseAsset: "BTC", QuoteAsset: "USDT", SettlementAsset: "USDT",
				ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001", PriceTick: "0.1", Status: "TRADING",
			}
			if market == "SWAP" {
				record.Linear, record.ContractValue, record.ContractValueAsset = true, "1", "BTC"
			}
			if err := tx.UpsertInstrument(record); err != nil {
				return err
			}
		}
		return nil
	}))
	service := &Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: &syncAdapter{}},
		SessionState: readySessionState(true),
		Fills:        &consumer.Reducer{Store: tradeStore},
		Now:          func() time.Time { return time.UnixMilli(4_000) },
	}
	return service, tradeStore
}

func dualOrder(market exchange.MarketType, filled string, status exchange.OrderStatus) exchange.Order {
	order := exchange.Order{
		Market: market, ExchangeOrderID: "100", ClientOrderID: "same-client", ExchangeSymbol: "BTCUSDT",
		OrderType: exchange.OrderTypeMarket, Side: exchange.SideBuy,
		Quantity: shared.MustDecimal("1"), FilledQuantity: shared.MustDecimal(filled), AveragePrice: shared.MustDecimal("100"),
		Status: status, CreatedAt: time.UnixMilli(1_000), UpdatedAt: time.UnixMilli(2_000),
	}
	if market == exchange.MarketTypeSwap {
		order.PositionSide = exchange.PositionSideNet
	}
	return order
}

// 同号不同市场的外部订单各自导入、各自更新，互不覆盖；合约成交只让合约钱包等确认，现货钱包不受影响。
func TestSameNamedOrdersInTwoMarketsImportAndUpdateSeparately(t *testing.T) {
	ctx := context.Background()
	service, tradeStore := newDualMarketSync(t)

	require.NoError(t, service.ApplyOrder(ctx, "account-1", dualOrder(exchange.MarketTypeSpot, "0", exchange.OrderStatusOpen)))
	require.NoError(t, service.ApplyOrder(ctx, "account-1", dualOrder(exchange.MarketTypeSwap, "0", exchange.OrderStatusOpen)))

	spot, err := tradeStore.GetOrderByClientID(ctx, "space-1", "account-1", exchange.MarketTypeSpot, "same-client")
	require.NoError(t, err)
	swap, err := tradeStore.GetOrderByClientID(ctx, "space-1", "account-1", exchange.MarketTypeSwap, "same-client")
	require.NoError(t, err)
	require.NotEqual(t, spot.OrderID, swap.OrderID, "同号的现货单和合约单是两条记录")
	require.Equal(t, "SPOT", spot.MarketType)
	require.Equal(t, "SWAP", swap.MarketType)

	// 合约订单撤销只改合约那一条。
	require.NoError(t, service.ApplyOrder(ctx, "account-1", dualOrder(exchange.MarketTypeSwap, "0", exchange.OrderStatusCanceled)))
	spot, err = tradeStore.GetOrderByClientID(ctx, "space-1", "account-1", exchange.MarketTypeSpot, "same-client")
	require.NoError(t, err)
	swap, err = tradeStore.GetOrderByClientID(ctx, "space-1", "account-1", exchange.MarketTypeSwap, "same-client")
	require.NoError(t, err)
	require.Equal(t, "OPEN", spot.State)
	require.Equal(t, "CANCELED", swap.State)

	// 合约成交：水位只记在合约钱包上。
	fill := exchange.Fill{
		Market: exchange.MarketTypeSwap, ExchangeTradeID: "t-1", ExchangeOrderID: "100", ClientOrderID: "same-client",
		ExchangeSymbol: "BTCUSDT", Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
		Quantity: shared.MustDecimal("0.5"), Price: shared.MustDecimal("100"), SettlementAsset: "USDT", TradedAt: time.UnixMilli(3_000),
	}
	require.NoError(t, service.ApplyOrder(ctx, "account-1", dualOrder(exchange.MarketTypeSwap, "0.5", exchange.OrderStatusPartiallyFilled)))
	applied, err := service.ApplyFill(ctx, "account-1", fill)
	require.NoError(t, err)
	require.True(t, applied)
	account, err := tradeStore.GetTradingAccountByID(ctx, "account-1")
	require.NoError(t, err)
	require.Equal(t, map[string]int64{"FUTURES": 3_000}, account.BalancePending, "合约成交不让现货钱包等确认")
}
