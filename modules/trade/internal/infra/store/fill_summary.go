package store

import (
	"context"
	"fmt"
	"sort"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

// FillSummary 是一段时间内成交的汇总：手续费按手续费资产分别汇总（不折算），已实现盈亏按结算资产汇总。
type FillSummary struct {
	Fees        []AssetAmount
	RealizedPnL []AssetAmount
	FillCount   int64
}

// AssetAmount 是某个资产上的一笔汇总金额。
type AssetAmount struct {
	Asset  string
	Amount shared.Decimal
}

// FillSummaryQuery 选出要汇总的成交：执行账户或组合账户（按成交所属订单的组合，已移出的成员的历史成交也算在组合下）二选一。
type FillSummaryQuery struct {
	TradingAccountID string
	LogicalAccountID string
	StartTime        int64
	EndTime          int64
}

// 原始成交和清理时累加进小时汇总表的成交合在一条语句里读（同一个快照，清理事务提交在两次读之间也不会重复计数）。
// 每笔成交只在其中一处，直接相加；汇总行按整点取舍（c_bucket_time 落在区间内的整个小时都算），区间两端不在整点时
// 已清理的那部分成交会多算或少算不足一小时，这是已知差别。原始成交每行算一笔，汇总行带笔数。
const (
	sqlSummarizeFillsByAccount = `
		SELECT c_fee, c_fee_asset, c_settlement_asset, c_realized_pnl, 1 AS c_fill_count
		FROM t_order_fills
		WHERE c_space_id = ? AND c_trading_account_id = ? AND c_traded_at >= ? AND c_traded_at <= ?
		UNION ALL
		SELECT c_fee, c_fee_asset, c_settlement_asset, c_realized_pnl, c_fill_count
		FROM t_order_fill_hourly_summaries
		WHERE c_space_id = ? AND c_trading_account_id = ? AND c_bucket_time >= ? AND c_bucket_time <= ?`
	sqlSummarizeFillsByLogical = `
		SELECT c_fee, c_fee_asset, c_settlement_asset, c_realized_pnl, 1 AS c_fill_count
		FROM t_order_fills
		WHERE c_space_id = ? AND c_logical_account_id = ? AND c_traded_at >= ? AND c_traded_at <= ?
		UNION ALL
		SELECT c_fee, c_fee_asset, c_settlement_asset, c_realized_pnl, c_fill_count
		FROM t_order_fill_hourly_summaries
		WHERE c_space_id = ? AND c_logical_account_id = ? AND c_bucket_time >= ? AND c_bucket_time <= ?`
)

// SummarizeFills 逐行读成交与小时汇总，用十进制累加（金额是字符串，不能让数据库按浮点求和）。
// 只有管理台的收益拆解用，经只读视图调用。
func (s *Store) SummarizeFills(ctx context.Context, spaceID string, query FillSummaryQuery) (FillSummary, error) {
	if (query.TradingAccountID == "") == (query.LogicalAccountID == "") {
		return FillSummary{}, fmt.Errorf("%w: 汇总成交要且只要给执行账户或组合账户之一", ErrInvalidRecord)
	}
	end := query.EndTime
	if end <= 0 {
		end = 1<<62 - 1
	}
	statement, owner := sqlSummarizeFillsByAccount, query.TradingAccountID
	if query.LogicalAccountID != "" {
		statement, owner = sqlSummarizeFillsByLogical, query.LogicalAccountID
	}
	rows, err := s.db.WithContext(ctx).Raw(statement, spaceID, owner, query.StartTime, end, spaceID, owner, query.StartTime, end).Rows()
	if err != nil {
		return FillSummary{}, err
	}
	defer rows.Close()
	fees, pnls := map[string]shared.Decimal{}, map[string]shared.Decimal{}
	var count int64
	for rows.Next() {
		var fee, feeAsset, settlementAsset, realized string
		var fills int64
		if err := rows.Scan(&fee, &feeAsset, &settlementAsset, &realized, &fills); err != nil {
			return FillSummary{}, err
		}
		value, err := parseFillSummaryValue(fee, realized, fills)
		if err != nil {
			return FillSummary{}, err
		}
		count += value.Count
		if !value.Fee.IsZero() {
			fees[feeAsset] = fees[feeAsset].Add(value.Fee)
		}
		if !value.RealizedPnL.IsZero() {
			pnls[settlementAsset] = pnls[settlementAsset].Add(value.RealizedPnL)
		}
	}
	if err := rows.Err(); err != nil {
		return FillSummary{}, err
	}
	return FillSummary{Fees: sortedAmounts(fees), RealizedPnL: sortedAmounts(pnls), FillCount: count}, nil
}

func sortedAmounts(values map[string]shared.Decimal) []AssetAmount {
	result := make([]AssetAmount, 0, len(values))
	for asset, amount := range values {
		result = append(result, AssetAmount{Asset: asset, Amount: amount})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Asset < result[j].Asset })
	return result
}
