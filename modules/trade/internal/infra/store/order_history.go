package store

import (
	"context"
	"encoding/json"
	"fmt"
)

// DeleteFinishedOrdersBefore 删掉一个执行账户里结束时间早于 before（毫秒）的一批已结束订单（最多 limit 单）
// 和它们的全部成交，返回删掉的订单数与成交数。订单和成交在同一个事务里一起删：只删成交会让订单看起来
// “成交没补齐”，只删订单违反外键。按（空间，执行账户，结束时间）定位，走 idx_trade_orders_account_finished。
// 哪些账户可以清理由调用方决定（见 runtime.OrderHistoryPruner）。
func (s *Store) DeleteFinishedOrdersBefore(ctx context.Context, spaceID, tradingAccountID string, before int64, limit int) (int64, int64, error) {
	if blank(spaceID) || blank(tradingAccountID) || before <= 0 || limit <= 0 {
		return 0, 0, fmt.Errorf("%w: incomplete order history prune request", ErrInvalidRecord)
	}
	var orders, fills int64
	err := s.Transaction(ctx, func(tx *Tx) error {
		var ids []string
		if err := tx.db.Raw(queryFinishedOrdersBefore, spaceID, tradingAccountID, before, activeOrderStatesJSON, limit).
			Scan(&ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		encoded, err := json.Marshal(ids)
		if err != nil {
			return fmt.Errorf("%w: encode pruned order ids", ErrInvalidRecord)
		}
		deletedFills := tx.db.Exec(execDeleteFillsOfOrders, spaceID, string(encoded))
		if err := writeError(deletedFills.Error); err != nil {
			return err
		}
		deletedOrders := tx.db.Exec(execDeleteOrdersByID, spaceID, string(encoded))
		if err := writeError(deletedOrders.Error); err != nil {
			return err
		}
		fills, orders = deletedFills.RowsAffected, deletedOrders.RowsAffected
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return orders, fills, nil
}

// 以下 SQL 整条写成常量，不在调用处拼接或内联。订单号经 json_each(?) 以不相关子查询传入。
const (
	// 已结束的单才有结束时间；状态再排除一次未结束状态（activeOrderStatesJSON），不依赖结束时间的写法。
	queryFinishedOrdersBefore = `SELECT c_order_id FROM t_trade_orders
		WHERE c_space_id = ? AND c_trading_account_id = ? AND c_finished_at > 0 AND c_finished_at < ?
			AND c_state NOT IN (SELECT value FROM json_each(?))
		ORDER BY c_finished_at
		LIMIT ?`
	execDeleteFillsOfOrders = `DELETE FROM t_order_fills
		WHERE c_space_id = ? AND c_order_id IN (SELECT value FROM json_each(?))`
	execDeleteOrdersByID = `DELETE FROM t_trade_orders
		WHERE c_space_id = ? AND c_order_id IN (SELECT value FROM json_each(?))`
)
