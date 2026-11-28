package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

const testHour = int64(3600 * 1000)

// summarySeedOrder 是汇总测试里的一单：所属组合（空串为不属于组合）、状态、结束时间和它的成交。
type summarySeedOrder struct {
	id       string
	logical  string
	state    string
	finished int64
	fills    []summarySeedFill
}

type summarySeedFill struct {
	at       int64
	fee      string
	feeAsset string
	realized string
}

// seedSummaryOrders 建一个纯合约模拟账户（加入组合 logical-1）和给定的订单、成交。
func seedSummaryOrders(t *testing.T, s *Store, orders []summarySeedOrder) {
	t.Helper()
	ctx := context.Background()
	account := testAccount()
	account.Markets, account.MarginMode = []string{"swap"}, "CROSS"
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		if err := tx.CreateTradingAccount(account); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "logical",
			Environment: "PAPER", SettlementAsset: "USDT", PauseReason: "x",
		}); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: "account-1", Enabled: true, Priority: 1,
		}); err != nil {
			return err
		}
		if err := tx.UpsertInstrument(InstrumentRecord{
			Exchange: "binance", Environment: "PRODUCTION", MarketType: "swap", ExchangeSymbol: "BTCUSDT", InstrumentID: "BTC-USDT-SWAP",
			BaseAsset: "BTC", QuoteAsset: "USDT", SettlementAsset: "USDT", Linear: true, ContractValue: "1", ContractValueAsset: "BTC",
			PriceTick: "0.01", ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001", Status: "TRADING",
		}); err != nil {
			return err
		}
		for _, order := range orders {
			owner := "OPERATOR"
			if order.logical == "" {
				owner = "EXTERNAL"
			}
			if err := tx.CreateOrder(OrderRecord{
				SpaceID: "space-1", TradingAccountID: "account-1", OrderID: order.id, ClientOrderID: order.id, Exchange: "binance", MarketType: "swap",
				ExchangeSymbol: "BTCUSDT", InstrumentID: "BTC-USDT-SWAP", OrderType: "MARKET", Side: "BUY", PositionSide: "NET",
				Quantity: "100", ReferencePrice: "100", OwnerType: owner, OwnerID: order.id, LogicalAccountID: order.logical, State: "OPEN", Version: 1,
			}); err != nil {
				return err
			}
			for i, fill := range order.fills {
				if _, err := tx.InsertFill(FillRecord{
					SpaceID: "space-1", TradingAccountID: "account-1", FillID: fmt.Sprintf("%s-f%d", order.id, i),
					ExchangeTradeID: fmt.Sprintf("%s-t%d", order.id, i), OrderID: order.id, Exchange: "binance", MarketType: "swap",
					InstrumentID: "BTC-USDT-SWAP", ExchangeSymbol: "BTCUSDT", Side: "BUY", PositionSide: "NET",
					Price: "100", Quantity: "1", SettlementAsset: "USDT",
					Fee: fill.fee, FeeAsset: fill.feeAsset, RealizedPnL: fill.realized, TradedAt: fill.at,
				}); err != nil {
					return err
				}
			}
			if err := tx.db.Exec(`UPDATE t_trade_orders SET c_state = ?, c_finished_at = ? WHERE c_order_id = ?`,
				order.state, order.finished, order.id).Error; err != nil {
				return err
			}
		}
		return nil
	}))
}

func summarize(t *testing.T, s *Store, query FillSummaryQuery) FillSummary {
	t.Helper()
	summary, err := s.SummarizeFills(context.Background(), "space-1", query)
	require.NoError(t, err)
	return summary
}

// 清理前后，按整小时对齐的区间的收益拆解（手续费、已实现盈亏、成交笔数）完全相同：执行账户和组合两个维度都一样。
// 覆盖多批清理、多种手续费资产、同一个小时桶被两轮清理累加、长期没结束的订单的早期成交留在成交表里、不属于组合的成交。
func TestPrunedFillsKeepPnlSummary(t *testing.T) {
	s := openTestStore(t)
	base := 1_000 * testHour
	orders := []summarySeedOrder{
		{id: "o1", logical: "logical-1", state: "FILLED", finished: base + 2*testHour, fills: []summarySeedFill{
			{base + 10, "0.1", "USDT", "1.5"}, {base + 20, "0.0001", "BNB", "-0.25"}, {base + testHour + 5, "0.2", "USDT", "0"},
		}},
		// o2 与 o1 的第一笔落在同一个小时桶，但在第二轮才被清理：桶要被两轮累加。
		{id: "o2", logical: "logical-1", state: "FILLED", finished: base + 5*testHour, fills: []summarySeedFill{
			{base + 30, "0.3", "USDT", "2"}, {base + 4*testHour, "0.0002", "BNB", "0.75"},
		}},
		// 不属于组合的成交：执行账户维度算，组合维度不算。
		{id: "ext", state: "FILLED", finished: base + 3*testHour, fills: []summarySeedFill{
			{base + 40, "1", "USDT", "10"}, {base + 2*testHour + 1, "0.5", "USDT", "-3"},
		}},
		// 长期没结束的订单：早期成交留在成交表里，不进汇总。
		{id: "open", logical: "logical-1", state: "PARTIALLY_FILLED", fills: []summarySeedFill{
			{base + 50, "0.4", "USDT", "4"},
		}},
		// 边界之后结束的单不清理。
		{id: "recent", logical: "logical-1", state: "FILLED", finished: base + 20*testHour, fills: []summarySeedFill{
			{base + 6*testHour, "0.6", "USDT", "6"},
		}},
	}
	seedSummaryOrders(t, s, orders)

	queries := map[string]FillSummaryQuery{
		"执行账户":    {TradingAccountID: "account-1", StartTime: base, EndTime: base + 10*testHour - 1},
		"组合":      {LogicalAccountID: "logical-1", StartTime: base, EndTime: base + 10*testHour - 1},
		"执行账户后半段": {TradingAccountID: "account-1", StartTime: base + 2*testHour, EndTime: base + 10*testHour - 1},
		"组合前一小时":  {LogicalAccountID: "logical-1", StartTime: base, EndTime: base + testHour - 1},
	}
	before := map[string]FillSummary{}
	for name, query := range queries {
		before[name] = summarize(t, s, query)
	}
	require.Equal(t, int64(9), before["执行账户"].FillCount)
	require.Equal(t, int64(7), before["组合"].FillCount)

	ctx := context.Background()
	// 第一轮：边界在 o1、ext 结束之后、o2 结束之前，每批 1 单。
	for {
		deleted, _, err := s.DeleteFinishedOrdersBefore(ctx, "space-1", "account-1", base+4*testHour, 1)
		require.NoError(t, err)
		if deleted == 0 {
			break
		}
	}
	for name, query := range queries {
		require.Equal(t, before[name], summarize(t, s, query), "第一轮之后 "+name)
	}
	// 第二轮把 o2 也清掉，与 o1 同一个小时桶累加。
	deletedOrders, deletedFills, err := s.DeleteFinishedOrdersBefore(ctx, "space-1", "account-1", base+10*testHour, 300)
	require.NoError(t, err)
	require.Equal(t, [2]int64{1, 2}, [2]int64{deletedOrders, deletedFills})
	for name, query := range queries {
		require.Equal(t, before[name], summarize(t, s, query), "第二轮之后 "+name)
	}

	var remaining []string
	require.NoError(t, s.db.Raw(`SELECT DISTINCT c_order_id FROM t_order_fills ORDER BY c_order_id`).Scan(&remaining).Error)
	require.Equal(t, []string{"open", "recent"}, remaining)

	type summaryRow struct {
		BucketTime       int64  `gorm:"column:c_bucket_time"`
		LogicalAccountID string `gorm:"column:c_logical_account_id"`
		FeeAsset         string `gorm:"column:c_fee_asset"`
		Fee              string `gorm:"column:c_fee"`
		RealizedPnL      string `gorm:"column:c_realized_pnl"`
		Count            int64  `gorm:"column:c_fill_count"`
	}
	var rows []summaryRow
	require.NoError(t, s.db.Raw(`SELECT c_bucket_time, c_logical_account_id, c_fee_asset, c_fee, c_realized_pnl, c_fill_count
		FROM t_order_fill_hourly_summaries ORDER BY c_bucket_time, c_logical_account_id, c_fee_asset`).Scan(&rows).Error)
	require.Equal(t, []summaryRow{
		{base, "", "USDT", "1", "10", 1},
		{base, "logical-1", "BNB", "0.0001", "-0.25", 1},
		{base, "logical-1", "USDT", "0.4", "3.5", 2}, // o1 第一笔 + o2 第一笔，两轮累加
		{base + testHour, "logical-1", "USDT", "0.2", "0", 1},
		{base + 2*testHour, "", "USDT", "0.5", "-3", 1},
		{base + 4*testHour, "logical-1", "BNB", "0.0002", "0.75", 1},
	}, rows)
}
