package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// 只删这个账户里结束时间早于边界、且状态已结束的订单，连同它们的成交；未结束的、边界之后结束的、别的账户的都保留。
func TestDeleteFinishedOrdersBefore(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const before = int64(10_000)
	other := testAccount()
	other.TradingAccountID, other.Name = "account-2", "other"
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		if err := tx.CreateTradingAccount(testAccount()); err != nil {
			return err
		}
		return tx.CreateTradingAccount(other)
	}))
	// 订单号 → 账户、状态、结束时间、成交笔数
	type seed struct {
		account  string
		state    string
		finished int64
		fills    int
	}
	seeds := map[string]seed{
		"old-filled":        {"account-1", "FILLED", 1_000, 2},
		"old-canceled":      {"account-1", "CANCELED", 9_999, 0},
		"at-boundary":       {"account-1", "FILLED", before, 1},
		"recent-filled":     {"account-1", "FILLED", 20_000, 1},
		"old-open":          {"account-1", "OPEN", 0, 0},
		"old-partial":       {"account-1", "PARTIALLY_FILLED", 1_000, 1},
		"other-account-old": {"account-2", "FILLED", 1_000, 1},
	}
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		for id, current := range seeds {
			if err := tx.CreateOrder(OrderRecord{
				SpaceID: "space-1", OrderID: id, TradingAccountID: current.account,
				ClientOrderID: "client-" + id, ExchangeOrderID: "exchange-" + id,
				ExchangeSymbol: "BTCUSDT", OrderType: "LIMIT", TimeInForce: "GTC",
				Side: "BUY", Quantity: "1", LimitPrice: stringPointer("100"), ReferencePrice: "100",
				OwnerType: "EXTERNAL", OwnerID: "exchange-" + id, State: "OPEN", Version: 1,
			}); err != nil {
				return err
			}
			if err := tx.db.Exec(`UPDATE t_trade_orders SET c_state = ?, c_finished_at = ? WHERE c_order_id = ?`,
				current.state, current.finished, id).Error; err != nil {
				return err
			}
			for i := 0; i < current.fills; i++ {
				if err := tx.db.Exec(execInsertOrderFill,
					"space-1", fmt.Sprintf("fill-%s-%d", id, i), fmt.Sprintf("trade-%s-%d", id, i), id,
					"exchange-"+id, current.account, "BINANCE", "SPOT",
					"BTC-USDT", "BTCUSDT", "BUY", "", "100", "0.5", "0",
					"USDT", "USDT", "0", "TAKER", 500+i, nil,
				).Error; err != nil {
					return err
				}
			}
		}
		return nil
	}))

	// 每批 1 单：先删结束最早的，再删下一单，第三批没有可删的。
	orders, fills, err := s.DeleteFinishedOrdersBefore(ctx, "space-1", "account-1", before, 1)
	require.NoError(t, err)
	require.Equal(t, [2]int64{1, 2}, [2]int64{orders, fills})
	orders, fills, err = s.DeleteFinishedOrdersBefore(ctx, "space-1", "account-1", before, 1)
	require.NoError(t, err)
	require.Equal(t, [2]int64{1, 0}, [2]int64{orders, fills})
	orders, fills, err = s.DeleteFinishedOrdersBefore(ctx, "space-1", "account-1", before, 1)
	require.NoError(t, err)
	require.Equal(t, [2]int64{0, 0}, [2]int64{orders, fills})

	var remaining []string
	require.NoError(t, s.db.Raw(`SELECT c_order_id FROM t_trade_orders ORDER BY c_order_id`).Scan(&remaining).Error)
	require.Equal(t, []string{"at-boundary", "old-open", "old-partial", "other-account-old", "recent-filled"}, remaining)
	var fillOrders []string
	require.NoError(t, s.db.Raw(`SELECT DISTINCT c_order_id FROM t_order_fills ORDER BY c_order_id`).Scan(&fillOrders).Error)
	require.Equal(t, []string{"at-boundary", "old-partial", "other-account-old", "recent-filled"}, fillOrders)
}
