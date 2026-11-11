package store

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// seedTargetChildren 建一个组合和同一目标下的几笔限价 GTC 子单，改成给定的状态和交易所单号（键：订单号，值：状态、交易所单号），
// 返回子单的内部标的 ID。
func seedTargetChildren(t *testing.T, s *Store, children map[string][2]string) string {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, seedAccountAndInstrument(ctx, s))
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		if err := tx.CreateLogicalAccount(LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "logical",
			OwnerInstanceID: "runner-1", OwnerSessionID: "session-1", ExecutionMode: "PAPER",
			SettlementAsset: "USDT", AutomationState: "PAUSED", PauseReason: "configure",
		}); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: "account-1", Enabled: true,
		}); err != nil {
			return err
		}
		for orderID := range children {
			limit := "99"
			if err := tx.CreateOrder(OrderRecord{
				SpaceID: "space-1", OrderID: orderID, TradingAccountID: "account-1", ClientOrderID: orderID,
				ExchangeSymbol: "BTCUSDT", OrderType: "LIMIT", TimeInForce: "GTC", LimitPrice: &limit,
				Side: "BUY", Quantity: "1", ReferencePrice: "100", OwnerType: "TARGET", OwnerID: "target-1",
				LogicalAccountID: "logical-1", RunnerID: "runner-1", State: "PENDING",
			}); err != nil {
				return err
			}
		}
		return nil
	}))
	for orderID, value := range children {
		require.NoError(t, s.DBForTest().Exec(
			`UPDATE t_trade_orders SET c_state = ?, c_exchange_order_id = ?, c_submitted_at = 1000 WHERE c_order_id = ?`,
			value[0], value[1], orderID).Error)
	}
	var instrumentID string
	require.NoError(t, s.DBForTest().Raw(`SELECT c_instrument_id FROM t_trade_orders LIMIT 1`).Scan(&instrumentID).Error)
	require.NotEmpty(t, instrumentID)
	return instrumentID
}

// 从没发到交易所的 GTC 单（查无此单退回待提交后丢弃，没有交易所单号）不算挂过：这个标的下一轮照样可以挂单。
func TestTargetEndedRestingInstrumentsSkipsNeverSentOrders(t *testing.T) {
	s := openTestStore(t)
	instrumentID := seedTargetChildren(t, s, map[string][2]string{"never-sent": {"CANCELED", ""}})
	ended, err := s.TargetEndedRestingInstruments(context.Background(), "space-1", "logical-1", "target-1")
	require.NoError(t, err)
	require.Empty(t, ended)

	require.NoError(t, s.DBForTest().Exec(`UPDATE t_trade_orders SET c_exchange_order_id = 'exchange-1' WHERE c_order_id = 'never-sent'`).Error)
	ended, err = s.TargetEndedRestingInstruments(context.Background(), "space-1", "logical-1", "target-1")
	require.NoError(t, err)
	require.Contains(t, ended, instrumentID)
}

// 三条按目标查子单的查询都走 idx_trade_orders_logical_owner_state（只扫这一份目标的子单），不走只按组合和提交时间的
// idx_trade_orders_target_hourly。
func TestTargetChildQueriesUseOwnerStateIndex(t *testing.T) {
	s := openTestStore(t)
	for name, query := range map[string]string{
		"TargetChildOrders":             sqlTargetChildOrders,
		"TargetEndedRestingInstruments": sqlTargetEndedRestingInstruments,
		"RejectedTargetAccounts":        sqlRejectedTargetAccounts,
	} {
		args := []any{"space-1", "logical-1", "target-1"}
		if name != "TargetEndedRestingInstruments" {
			args = append(args, "BTC-USDT-SPOT")
		}
		var plan []struct {
			Detail string `gorm:"column:detail"`
		}
		require.NoError(t, s.DBForTest().Raw("EXPLAIN QUERY PLAN "+query, args...).Scan(&plan).Error)
		var details []string
		for _, row := range plan {
			details = append(details, row.Detail)
		}
		require.Contains(t, strings.Join(details, "\n"), "idx_trade_orders_logical_owner_state", name)
	}
}

// TargetChildOrders 只返回提交过的子单，按提交先后排序。
func TestTargetChildOrdersReturnsSubmittedChildren(t *testing.T) {
	s := openTestStore(t)
	instrumentID := seedTargetChildren(t, s, map[string][2]string{"sent": {"OPEN", "exchange-1"}, "pending": {"PENDING", ""}})
	require.NoError(t, s.DBForTest().Exec(`UPDATE t_trade_orders SET c_submitted_at = 0 WHERE c_order_id = 'pending'`).Error)
	children, err := s.TargetChildOrders(context.Background(), "space-1", "logical-1", "target-1", instrumentID)
	require.NoError(t, err)
	require.Len(t, children, 1)
	require.Equal(t, "exchange-1", children[0].ExchangeOrderID)
	require.Equal(t, "OPEN", children[0].State)
}
