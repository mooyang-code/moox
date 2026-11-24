package store

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// queryPlan 返回一条查询的 EXPLAIN QUERY PLAN 明细，逐行用换行连起来。
func queryPlan(t *testing.T, s *Store, query string, args ...any) string {
	t.Helper()
	var plan []struct {
		Detail string `gorm:"column:detail"`
	}
	require.NoError(t, s.DBForTest().Raw("EXPLAIN QUERY PLAN "+query, args...).Scan(&plan).Error)
	details := make([]string, 0, len(plan))
	for _, row := range plan {
		details = append(details, row.Detail)
	}
	return strings.Join(details, "\n")
}

// 随历史订单、成交增长的高频查询都走预期的索引：只读当前需要的那部分行，不扫账户或空间的全部订单、成交。
func TestHotQueriesUseIntendedIndexes(t *testing.T) {
	s := openTestStore(t)
	for _, c := range []struct {
		name  string
		query string
		args  []any
		want  string
	}{
		{"成交缺口", queryOrdersWithFillGap, []any{"space-1", "account-1"}, "USING INDEX idx_trade_orders_fill_gap"},
		{"下架标的的成交缺口", queryInstrumentOrdersWithFillGap, []any{"space-1", "account-1", "SPOT", "BTCUSDT"},
			"USING INDEX idx_trade_orders_account_symbol_finished"},
		{"受管标的", queryManagedSymbols, []any{"space-1", "account-1"}, "USING INDEX sqlite_autoindex_t_account_instrument_summaries_1 (c_space_id=? AND c_trading_account_id=?)"},
		{"还没提交完的预留", queryInFlightReservations, []any{"space-1", "account-1", "USDT", `["SPOT"]`},
			"USING INDEX idx_trade_orders_account_state (c_space_id=? AND c_trading_account_id=? AND c_state=?)"},
		{"快照之后提交的预留", querySubmittedReservationsSince, []any{"space-1", "account-1", int64(1), "USDT", `["SPOT"]`},
			"USING INDEX idx_trade_orders_account_submitted (c_space_id=? AND c_trading_account_id=? AND c_submitted_at>?)"},
		{"按组合汇总成交", sqlSummarizeFillsByLogical, []any{"space-1", "logical-1", int64(0), int64(1), "space-1", "logical-1", int64(0), int64(1)},
			"USING INDEX idx_order_fills_logical_time (c_space_id=? AND c_logical_account_id=? AND c_traded_at>? AND c_traded_at<?)"},
		{"按组合汇总成交读小时汇总", sqlSummarizeFillsByLogical, []any{"space-1", "logical-1", int64(0), int64(1), "space-1", "logical-1", int64(0), int64(1)},
			"USING INDEX idx_order_fill_hourly_summaries_logical (c_space_id=? AND c_logical_account_id=? AND c_bucket_time>? AND c_bucket_time<?)"},
		{"按执行账户汇总成交", sqlSummarizeFillsByAccount, []any{"space-1", "account-1", int64(0), int64(1), "space-1", "account-1", int64(0), int64(1)},
			"USING INDEX idx_order_fills_paper_balance_history (c_space_id=? AND c_trading_account_id=? AND c_traded_at>? AND c_traded_at<?)"},
		{"按执行账户汇总成交读小时汇总", sqlSummarizeFillsByAccount, []any{"space-1", "account-1", int64(0), int64(1), "space-1", "account-1", int64(0), int64(1)},
			"USING INDEX sqlite_autoindex_t_order_fill_hourly_summaries_1 (c_space_id=? AND c_trading_account_id=? AND c_bucket_time>? AND c_bucket_time<?)"},
		{"历史订单清理读成交做汇总", queryFillsOfOrdersForSummary, []any{"space-1", `["order-1"]`}, "idx_order_fills_order_time (c_space_id=? AND c_order_id=?)"},
		{"历史订单清理读已有汇总", queryFillHourlySummary, []any{"space-1", "account-1", int64(0), "", "USDT", "USDT"},
			"USING INDEX sqlite_autoindex_t_order_fill_hourly_summaries_1 (c_space_id=? AND c_trading_account_id=? AND c_bucket_time=? AND c_logical_account_id=? AND c_fee_asset=? AND c_settlement_asset=?)"},
		{"历史订单清理选单", queryFinishedOrdersBefore, []any{"space-1", "account-1", int64(1), activeOrderStatesJSON, 300},
			"USING INDEX idx_trade_orders_account_finished (c_space_id=? AND c_trading_account_id=? AND c_finished_at>? AND c_finished_at<?)"},
		{"历史订单清理删成交", execDeleteFillsOfOrders, []any{"space-1", `["order-1"]`}, "idx_order_fills_order_time (c_space_id=? AND c_order_id=?)"},
		{"历史订单清理删订单", execDeleteOrdersByID, []any{"space-1", `["order-1"]`}, "sqlite_autoindex_t_trade_orders_1 (c_space_id=? AND c_order_id=?)"},
	} {
		plan := queryPlan(t, s, c.query, c.args...)
		require.Contains(t, plan, c.want, c.name)
		require.NotContains(t, plan, "TEMP B-TREE FOR DISTINCT", c.name)
	}
}

// 管理台按组合分页查成交：沿 idx_order_fills_logical_time 倒序读一页，不对组合的全部成交建临时排序。
func TestLogicalFillPageReadsIndexInOrder(t *testing.T) {
	s := openTestStore(t)
	// 记下分页查询实际生成的 SQL 和参数（COUNT 那条不带 ORDER BY）。
	var statement string
	var args []any
	require.NoError(t, s.DBForTest().Callback().Query().After("gorm:query").Register("capture_fill_page", func(db *gorm.DB) {
		if strings.Contains(db.Statement.SQL.String(), "ORDER BY") {
			statement, args = db.Statement.SQL.String(), append([]any(nil), db.Statement.Vars...)
		}
	}))
	_, _, err := s.ListFills(context.Background(), "space-1", FillQuery{LogicalAccountID: "logical-1", Offset: 20, Limit: 20})
	require.NoError(t, err)
	plan := queryPlan(t, s, statement, args...)
	require.Contains(t, plan, "USING INDEX idx_order_fills_logical_time (c_space_id=? AND c_logical_account_id=?)", statement)
	require.NotContains(t, plan, "TEMP B-TREE", statement)
}

// 按组合查未结束订单（目标执行每轮都查好几次）：计数和取行都按状态走 idx_trade_orders_logical_state，
// 不沿创建时间索引或来源索引把组合的历史订单读一遍。
func TestOpenOrdersByLogicalUseStateIndex(t *testing.T) {
	s := openTestStore(t)
	type captured struct {
		statement string
		args      []any
	}
	var statements []captured
	require.NoError(t, s.DBForTest().Callback().Query().After("gorm:query").Register("capture_open_orders", func(db *gorm.DB) {
		statements = append(statements, captured{db.Statement.SQL.String(), append([]any(nil), db.Statement.Vars...)})
	}))
	for _, query := range []OrderQuery{
		{LogicalAccountID: "logical-1", OnlyOpen: true, Limit: 1000},
		{LogicalAccountID: "logical-1", OwnerType: "TARGET", OnlyOpen: true, Limit: 1000},
		{LogicalAccountID: "logical-1", TradingAccountID: "account-1", InstrumentID: "BTC-USDT", OnlyOpen: true, Limit: 50},
	} {
		statements = nil
		_, _, err := s.ListOrders(context.Background(), "space-1", query)
		require.NoError(t, err)
		require.Len(t, statements, 2, "一条计数、一条取行")
		for _, item := range statements {
			plan := queryPlan(t, s, item.statement, item.args...)
			require.Contains(t, plan, "idx_trade_orders_logical_state (c_space_id=? AND c_logical_account_id=? AND c_state=?)", item.statement)
		}
	}
}
