package equity

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// 模拟现货账户的受管单全部按保留期真删之后，权益照旧：HALT 停止交易、取不到价，按最近成交价降级估值，
// 这个价格和“受管”都来自标的汇总，不依赖已删的成交；清理后换一个没有内存最后已知价的服务实例再采样，权益一致。
func TestSampleAccountEquityUnchangedAfterPruningManagedOrders(t *testing.T) {
	ctx := context.Background()
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = tradeStore.Close() })
	now := time.UnixMilli(sourceMillisDegraded + 2)
	finished := now.Add(-10 * 24 * time.Hour)
	require.NoError(t, tradeStore.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "paper-1", Name: "paper",
			Exchange: string(exchange.ExchangeBinance), Markets: []string{"spot"},
			ExecutionMode: string(exchange.ExecutionModePaper), Environment: "PAPER", SettlementAsset: "USDT",
			Status: string(exchange.AccountStatusEnabled), Ready: true,
			Snapshot: store.TradingAccountSnapshot{
				Balances: []store.AssetBalance{
					{Wallet: "UNIFIED", Asset: "USDT", Available: "500", Total: "500"},
					{Wallet: "UNIFIED", Asset: "BTC", Available: "0.01", Total: "0.01"},
					{Wallet: "UNIFIED", Asset: "HALT", Available: "4", Total: "4"},
				},
				ExchangeUpdatedAt: sourceMillisDegraded,
			},
		}); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "logical", ExecutionMode: string(exchange.ExecutionModePaper),
			SettlementAsset: "USDT", AutomationState: "PAUSED", PauseReason: "test",
		}); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: "paper-1", Enabled: true,
		}); err != nil {
			return err
		}
		for asset, buy := range map[string]struct{ status, price, quantity string }{
			"BTC": {"TRADING", "40000", "0.01"}, "HALT": {"BREAK", "7", "4"},
		} {
			if err := tx.UpsertInstrument(store.InstrumentRecord{
				Exchange: string(exchange.ExchangeBinance), MarketType: "spot", ExchangeSymbol: asset + "USDT",
				InstrumentID: asset + "-USDT-SPOT", BaseAsset: asset, QuoteAsset: "USDT",
				ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001", PriceTick: "0.1", Status: buy.status,
			}); err != nil {
				return err
			}
			orderID := "bought-" + asset
			if err := tx.CreateOrder(store.OrderRecord{
				SpaceID: "space-1", TradingAccountID: "paper-1", OrderID: orderID, ClientOrderID: orderID,
				Exchange: string(exchange.ExchangeBinance), MarketType: "spot", ExchangeSymbol: asset + "USDT",
				InstrumentID: asset + "-USDT-SPOT", OrderType: "MARKET", Side: "BUY", Quantity: buy.quantity,
				ReferencePrice: buy.price, State: "FILLED", FinishedAt: finished.UnixMilli(),
				OwnerType: "OPERATOR", OwnerID: "action-" + asset, LogicalAccountID: "logical-1", Version: 1,
			}); err != nil {
				return err
			}
			if _, err := tx.InsertFill(store.FillRecord{
				SpaceID: "space-1", FillID: orderID, ExchangeTradeID: orderID, OrderID: orderID,
				Side: "BUY", Price: buy.price, Quantity: buy.quantity, Fee: "0", FeeAsset: "USDT",
				SettlementAsset: "USDT", RealizedPnL: "0", TradedAt: finished.UnixMilli(),
			}); err != nil {
				return err
			}
		}
		return nil
	}))
	adapter := &delistingPriceAdapter{at: time.UnixMilli(sourceMillisDegraded + 1)}
	sample := func(at time.Time) (string, string) {
		// 每次换一个服务实例：内存里没有最后已知价，降级只能靠标的汇总里的最近成交价。
		service := &Service{Store: tradeStore, Adapters: equityTestAdapters{adapter: adapter}, Now: func() time.Time { return at }}
		require.NoError(t, service.SampleAccount(ctx, "paper-1"))
		points, err := tradeStore.ListAccountEquityPoints(ctx, "space-1", "paper-1", 0, 0)
		require.NoError(t, err)
		require.NotEmpty(t, points)
		logical, err := allLogicalEquityPoints(tradeStore)
		require.NoError(t, err)
		require.NotEmpty(t, logical)
		return points[len(points)-1].Equity, logical[len(logical)-1].Equity
	}

	account, logical := sample(now)
	require.Equal(t, "1028", account, "USDT 500 + BTC 0.01×50000（报价）+ HALT 4×7（最近成交价）")
	require.Equal(t, "1028", logical)
	require.NotContains(t, adapter.seen, "HALTUSDT", "停止交易的标的不取价")

	orders, fills, err := tradeStore.DeleteFinishedOrdersBefore(ctx, "space-1", "paper-1", now.UnixMilli(), 100)
	require.NoError(t, err)
	require.Equal(t, int64(2), orders, "两笔受管单都删掉")
	require.Equal(t, int64(2), fills)
	_, total, err := tradeStore.ListFills(ctx, "space-1", store.FillQuery{TradingAccountID: "paper-1", Limit: 10})
	require.NoError(t, err)
	require.Zero(t, total)

	account, logical = sample(now.Add(time.Minute))
	require.Equal(t, "1028", account, "清理后受管判断与降级估值不变")
	require.Equal(t, "1028", logical)
}
