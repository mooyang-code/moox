package runtime

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// 模拟账户（现货、合约、混合）里结束超过保留天数（默认 5 天）的订单分批删掉，之内（含正好到边界）的保留；实盘账户不清理（见 TestOrderHistoryPrunableOnlyPaper）。
// 保留天数按账户各自的配置：0 天不清理，1 天的按 1 天算边界；边界不晚于模拟资金费补算的起点。
func TestOrderHistoryPrunerDeletesPaperOrdersOlderThanRetention(t *testing.T) {
	ctx := context.Background()
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tradeStore.Close()) })
	now := time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC)
	cutoff := now.Add(-time.Duration(tradingaccount.DefaultOrderRetentionDays) * 24 * time.Hour)
	finished := []time.Time{
		cutoff.Add(-30 * 24 * time.Hour), cutoff.Add(-time.Hour), cutoff.Add(-time.Millisecond), // 要删
		cutoff, now.Add(-time.Hour), // 保留
	}
	accounts := map[string][]string{"swap-paper": {"SWAP"}, "spot-paper": {"SPOT"}, "mixed-paper": {"SPOT", "SWAP"},
		"swap-keep": {"SWAP"}, "swap-short": {"SWAP"}, "swap-funded": {"SWAP"}, "swap-stale": {"SWAP"}}
	retention := map[string]int{"swap-keep": 0, "swap-short": 1}
	require.NoError(t, tradeStore.Transaction(ctx, func(tx *store.Tx) error {
		for id, markets := range accounts {
			if err := tx.CreateTradingAccount(store.TradingAccountRecord{
				SpaceID: "space-1", TradingAccountID: id, Name: id,
				Exchange: "BINANCE", Markets: markets, ExecutionMode: "PAPER", Environment: "PAPER",
				SettlementAsset: "USDT", Status: "ENABLED",
			}); err != nil {
				return err
			}
			if days, found := retention[id]; found {
				if err := tx.SetPaperOrderRetentionDays("space-1", id, days); err != nil {
					return err
				}
			}
			for i, at := range finished {
				orderID := fmt.Sprintf("%s-%d", id, i)
				if err := tx.CreateOrder(store.OrderRecord{
					SpaceID: "space-1", OrderID: orderID, TradingAccountID: id, ClientOrderID: "client-" + orderID,
					MarketType: markets[len(markets)-1], ExchangeSymbol: "BTCUSDT", OrderType: "MARKET", Side: "BUY",
					Quantity: "1", ReferencePrice: "100", OwnerType: "EXTERNAL", OwnerID: orderID,
					State: "CANCELED", Version: 1, FinishedAt: at.UnixMilli(),
				}); err != nil {
					return err
				}
			}
		}
		return nil
	}))

	// swap-funded 最近一次资金费结算在边界前 2 小时：补算从那个整点前一小时读成交，边界收紧到那里，只删得掉更早的 1 单。
	require.NoError(t, tradeStore.DBForTest().Exec(`INSERT INTO t_paper_funding_fees (c_space_id, c_trading_account_id, c_exchange_symbol,
		c_settled_at, c_funding_rate, c_mark_price, c_quantity, c_amount, c_asset) VALUES ('space-1', 'swap-funded', 'BTCUSDT', ?, '0', '100', '0', '0', 'USDT')`,
		cutoff.Add(-2*time.Hour).UnixMilli()).Error)
	// swap-stale 的资金费 20 天前就没再结算过：补算最多回看 7 天，边界停在 7 天前，30 天前那单照删。
	require.NoError(t, tradeStore.DBForTest().Exec(`INSERT INTO t_paper_funding_fees (c_space_id, c_trading_account_id, c_exchange_symbol,
		c_settled_at, c_funding_rate, c_mark_price, c_quantity, c_amount, c_asset) VALUES ('space-1', 'swap-stale', 'BTCUSDT', ?, '0', '100', '0', '0', 'USDT')`,
		now.Add(-20*24*time.Hour).UnixMilli()).Error)

	pruner := &OrderHistoryPruner{Store: tradeStore, Now: func() time.Time { return now }, batch: 2}
	require.NoError(t, pruner.RunAll(ctx))
	for id, want := range map[string]int64{"swap-paper": 2, "spot-paper": 2, "mixed-paper": 2, "swap-keep": 5, "swap-short": 1, "swap-funded": 4, "swap-stale": 4} {
		_, total, err := tradeStore.ListOrders(ctx, "space-1", store.OrderQuery{TradingAccountID: id})
		require.NoError(t, err)
		require.Equal(t, want, total, id)
	}
}

func TestOrderHistoryPrunableOnlyPaper(t *testing.T) {
	require.True(t, orderHistoryPrunable(store.TradingAccountRecord{ExecutionMode: "PAPER", Markets: []string{"SWAP"}}))
	require.True(t, orderHistoryPrunable(store.TradingAccountRecord{ExecutionMode: "PAPER", Markets: []string{"STOCK"}}))
	require.True(t, orderHistoryPrunable(store.TradingAccountRecord{ExecutionMode: "PAPER", Markets: []string{"SPOT", "SWAP"}}))
	require.False(t, orderHistoryPrunable(store.TradingAccountRecord{ExecutionMode: "LIVE", Markets: []string{"SPOT"}}))
	require.False(t, orderHistoryPrunable(store.TradingAccountRecord{ExecutionMode: "LIVE", Markets: []string{"SWAP"}}))
}

// 改订单保留天数不重建会话：会话配置比较不看保留天数，别的模拟配置照常比较。
func TestSessionConfigIgnoresOrderRetentionDays(t *testing.T) {
	left := store.TradingAccountRecord{TradingAccountID: "paper-1", ExecutionMode: "PAPER",
		PaperConfig: &store.PaperAccountConfigRecord{InitialBalance: "1000", SlippageBPS: "0", OrderRetentionDays: 5}}
	right := left
	config := *left.PaperConfig
	config.OrderRetentionDays = 0
	right.PaperConfig = &config
	require.True(t, sameSessionConfig(left, right))
	config.SlippageBPS = "1"
	require.False(t, sameSessionConfig(left, right))
}
