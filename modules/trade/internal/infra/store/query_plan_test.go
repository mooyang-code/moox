package store

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
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
		{"持币均价读一个标的的成交", querySymbolFillsAscending, []any{"space-1", "account-1", "SPOT", "BTCUSDT"},
			"USING INDEX sqlite_autoindex_t_order_fills_2 (c_space_id=? AND c_trading_account_id=? AND c_market_type=? AND c_exchange_symbol=?)"},
		{"成交缺口", queryOrdersWithFillGap, []any{"space-1", "account-1"}, "USING INDEX idx_trade_orders_fill_gap"},
		{"下架标的的成交缺口", queryInstrumentOrdersWithFillGap, []any{"space-1", "account-1", "SPOT", "BTCUSDT"},
			"USING INDEX idx_trade_orders_account_symbol_finished"},
		{"受管标的", queryManagedInstruments, []any{"space-1", "account-1"}, "USING COVERING INDEX idx_trade_orders_managed_instruments"},
		{"还没提交完的预留", queryInFlightReservations, []any{"space-1", "account-1", "USDT", `["SPOT"]`},
			"USING INDEX idx_trade_orders_account_state (c_space_id=? AND c_trading_account_id=? AND c_state=?)"},
		{"快照之后提交的预留", querySubmittedReservationsSince, []any{"space-1", "account-1", int64(1), "USDT", `["SPOT"]`},
			"USING INDEX idx_trade_orders_account_submitted (c_space_id=? AND c_trading_account_id=? AND c_submitted_at>?)"},
	} {
		plan := queryPlan(t, s, c.query, c.args...)
		require.Contains(t, plan, c.want, c.name)
		require.NotContains(t, plan, "TEMP B-TREE FOR DISTINCT", c.name)
	}
}
