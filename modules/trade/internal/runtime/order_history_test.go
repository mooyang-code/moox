package runtime

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// 纯合约模拟账户里结束超过 5 天的订单分批删掉，5 天内（含正好 5 天）的保留；现货模拟账户、实盘账户一律不清理。
func TestOrderHistoryPrunerDeletesOnlyPaperContractOrdersOlderThanFiveDays(t *testing.T) {
	ctx := context.Background()
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tradeStore.Close()) })
	now := time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC)
	cutoff := now.Add(-orderHistoryRetention)
	finished := []time.Time{
		cutoff.Add(-30 * 24 * time.Hour), cutoff.Add(-time.Hour), cutoff.Add(-time.Millisecond), // 要删
		cutoff, now.Add(-time.Hour), // 保留
	}
	accounts := map[string][]string{"swap-paper": {"SWAP"}, "spot-paper": {"SPOT"}, "mixed-paper": {"SPOT", "SWAP"}}
	require.NoError(t, tradeStore.Transaction(ctx, func(tx *store.Tx) error {
		for id, markets := range accounts {
			if err := tx.CreateTradingAccount(store.TradingAccountRecord{
				SpaceID: "space-1", TradingAccountID: id, Name: id,
				Exchange: "BINANCE", Markets: markets, ExecutionMode: "PAPER", Environment: "PAPER",
				SettlementAsset: "USDT", Status: "ENABLED",
			}); err != nil {
				return err
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

	pruner := &OrderHistoryPruner{Store: tradeStore, Now: func() time.Time { return now }, batch: 2}
	require.NoError(t, pruner.RunAll(ctx))
	for id, want := range map[string]int64{"swap-paper": 2, "spot-paper": 5, "mixed-paper": 5} {
		_, total, err := tradeStore.ListOrders(ctx, "space-1", store.OrderQuery{TradingAccountID: id})
		require.NoError(t, err)
		require.Equal(t, want, total, id)
	}
}

func TestOrderHistoryPrunableOnlyPaperWithoutBalanceMarket(t *testing.T) {
	require.True(t, orderHistoryPrunable(store.TradingAccountRecord{ExecutionMode: "PAPER", Markets: []string{"SWAP"}}))
	require.False(t, orderHistoryPrunable(store.TradingAccountRecord{ExecutionMode: "PAPER", Markets: []string{"STOCK"}}))
	require.False(t, orderHistoryPrunable(store.TradingAccountRecord{ExecutionMode: "PAPER", Markets: []string{"SPOT", "SWAP"}}))
	require.False(t, orderHistoryPrunable(store.TradingAccountRecord{ExecutionMode: "LIVE", Markets: []string{"SWAP"}}))
}
