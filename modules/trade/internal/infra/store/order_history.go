package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"gorm.io/gorm"
)

// fillSummaryBucket 是成交汇总的时间粒度：1 小时（毫秒）。
const fillSummaryBucket int64 = 3600 * 1000

// DeleteFinishedOrdersBefore 删掉一个执行账户里结束时间早于 before（毫秒）的一批已结束订单（最多 limit 单）
// 和它们的全部成交，返回删掉的订单数与成交数。订单和成交在同一个事务里一起删：只删成交会让订单看起来
// “成交没补齐”，只删订单违反外键。删成交之前，先把这些成交的手续费、已实现盈亏和笔数按小时累加进
// t_order_fill_hourly_summaries，收益拆解（SummarizeFills）照样算得到。按（空间，执行账户，结束时间）
// 定位，走 idx_trade_orders_account_finished。哪些账户可以清理由调用方决定（见 runtime.OrderHistoryPruner）。
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
		if err := summarizePrunedFills(tx.db, spaceID, string(encoded)); err != nil {
			return err
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

// fillSummaryKey 是汇总表的主键（不含空间）。
type fillSummaryKey struct {
	TradingAccountID string
	BucketTime       int64
	LogicalAccountID string
	FeeAsset         string
	SettlementAsset  string
}

type fillSummaryValue struct {
	Fee         shared.Decimal
	RealizedPnL shared.Decimal
	Count       int64
}

func (v fillSummaryValue) add(other fillSummaryValue) fillSummaryValue {
	return fillSummaryValue{Fee: v.Fee.Add(other.Fee), RealizedPnL: v.RealizedPnL.Add(other.RealizedPnL), Count: v.Count + other.Count}
}

// summarizePrunedFills 读出这批订单的成交，按（执行账户，整点，组合，手续费资产，结算资产）用十进制累加，
// 再与汇总表里已有的同键行相加后写回。必须在删成交的同一个事务里、删之前调用。
func summarizePrunedFills(db *gorm.DB, spaceID, encodedOrderIDs string) error {
	rows, err := db.Raw(queryFillsOfOrdersForSummary, spaceID, encodedOrderIDs).Rows()
	if err != nil {
		return err
	}
	sums := map[fillSummaryKey]fillSummaryValue{}
	for rows.Next() {
		var key fillSummaryKey
		var tradedAt int64
		var fee, realized string
		if err := rows.Scan(&key.TradingAccountID, &key.LogicalAccountID, &tradedAt, &fee, &key.FeeAsset, &key.SettlementAsset, &realized); err != nil {
			_ = rows.Close()
			return err
		}
		value, err := parseFillSummaryValue(fee, realized, 1)
		if err != nil {
			_ = rows.Close()
			return err
		}
		key.BucketTime = tradedAt - tradedAt%fillSummaryBucket
		sums[key] = sums[key].add(value)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	keys := make([]fillSummaryKey, 0, len(sums))
	for key := range sums {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return fillSummaryKeyLess(keys[i], keys[j]) })
	for _, key := range keys {
		var existing []struct {
			Fee         string `gorm:"column:c_fee"`
			RealizedPnL string `gorm:"column:c_realized_pnl"`
			Count       int64  `gorm:"column:c_fill_count"`
		}
		if err := db.Raw(queryFillHourlySummary, spaceID, key.TradingAccountID, key.BucketTime, key.LogicalAccountID,
			key.FeeAsset, key.SettlementAsset).Scan(&existing).Error; err != nil {
			return err
		}
		total := sums[key]
		if len(existing) > 0 {
			previous, err := parseFillSummaryValue(existing[0].Fee, existing[0].RealizedPnL, existing[0].Count)
			if err != nil {
				return err
			}
			total = total.add(previous)
		}
		if err := writeError(db.Exec(execUpsertFillHourlySummary, spaceID, key.TradingAccountID, key.BucketTime, key.LogicalAccountID,
			key.FeeAsset, key.SettlementAsset, total.Fee.String(), total.RealizedPnL.String(), total.Count).Error); err != nil {
			return err
		}
	}
	return nil
}

func parseFillSummaryValue(fee, realized string, count int64) (fillSummaryValue, error) {
	feeValue, err := shared.ParseDecimal(fee)
	if err != nil {
		return fillSummaryValue{}, fmt.Errorf("成交手续费 %q 不是合法数字：%w", fee, err)
	}
	pnlValue, err := shared.ParseDecimal(realized)
	if err != nil {
		return fillSummaryValue{}, fmt.Errorf("成交已实现盈亏 %q 不是合法数字：%w", realized, err)
	}
	return fillSummaryValue{Fee: feeValue, RealizedPnL: pnlValue, Count: count}, nil
}

func fillSummaryKeyLess(left, right fillSummaryKey) bool {
	if left.TradingAccountID != right.TradingAccountID {
		return left.TradingAccountID < right.TradingAccountID
	}
	if left.BucketTime != right.BucketTime {
		return left.BucketTime < right.BucketTime
	}
	if left.LogicalAccountID != right.LogicalAccountID {
		return left.LogicalAccountID < right.LogicalAccountID
	}
	if left.FeeAsset != right.FeeAsset {
		return left.FeeAsset < right.FeeAsset
	}
	return left.SettlementAsset < right.SettlementAsset
}

// 以下 SQL 整条写成常量，不在调用处拼接或内联。订单号经 json_each(?) 以不相关子查询传入。
const (
	// 已结束的单才有结束时间；状态再排除一次未结束状态（activeOrderStatesJSON），不依赖结束时间的写法。
	queryFinishedOrdersBefore = `SELECT c_order_id FROM t_trade_orders
		WHERE c_space_id = ? AND c_trading_account_id = ? AND c_finished_at > 0 AND c_finished_at < ?
			AND c_state NOT IN (SELECT value FROM json_each(?))
		ORDER BY c_finished_at
		LIMIT ?`
	// 成交的组合可能为空（订单不属于任何组合），汇总表里记成空串。
	queryFillsOfOrdersForSummary = `SELECT c_trading_account_id, COALESCE(c_logical_account_id, ''), c_traded_at,
			c_fee, c_fee_asset, c_settlement_asset, c_realized_pnl
		FROM t_order_fills
		WHERE c_space_id = ? AND c_order_id IN (SELECT value FROM json_each(?))`
	queryFillHourlySummary = `SELECT c_fee, c_realized_pnl, c_fill_count
		FROM t_order_fill_hourly_summaries
		WHERE c_space_id = ? AND c_trading_account_id = ? AND c_bucket_time = ? AND c_logical_account_id = ?
			AND c_fee_asset = ? AND c_settlement_asset = ?`
	execUpsertFillHourlySummary = `INSERT INTO t_order_fill_hourly_summaries (
			c_space_id, c_trading_account_id, c_bucket_time, c_logical_account_id,
			c_fee_asset, c_settlement_asset, c_fee, c_realized_pnl, c_fill_count
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (c_space_id, c_trading_account_id, c_bucket_time, c_logical_account_id, c_fee_asset, c_settlement_asset)
		DO UPDATE SET c_fee = excluded.c_fee, c_realized_pnl = excluded.c_realized_pnl, c_fill_count = excluded.c_fill_count`
	execDeleteFillsOfOrders = `DELETE FROM t_order_fills
		WHERE c_space_id = ? AND c_order_id IN (SELECT value FROM json_each(?))`
	execDeleteOrdersByID = `DELETE FROM t_trade_orders
		WHERE c_space_id = ? AND c_order_id IN (SELECT value FROM json_each(?))`
)
