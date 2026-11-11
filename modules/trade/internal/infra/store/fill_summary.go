package store

import (
	"context"
	"database/sql"
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

const (
	sqlSummarizeFillsByAccount = `
		SELECT c_fee, c_fee_asset, c_settlement_asset, c_realized_pnl
		FROM t_order_fills
		WHERE c_space_id = ? AND c_trading_account_id = ? AND c_traded_at >= ? AND c_traded_at <= ?`
	sqlSummarizeFillsByLogical = `
		SELECT c_fee, c_fee_asset, c_settlement_asset, c_realized_pnl
		FROM t_order_fills
		WHERE c_space_id = ? AND c_logical_account_id = ? AND c_traded_at >= ? AND c_traded_at <= ?`
)

// SummarizeFills 逐行读成交，用十进制累加（成交表里的金额是字符串，不能让数据库按浮点求和）。
// 只有管理台的收益拆解用，经只读视图调用。
func (s *Store) SummarizeFills(ctx context.Context, spaceID string, query FillSummaryQuery) (FillSummary, error) {
	if (query.TradingAccountID == "") == (query.LogicalAccountID == "") {
		return FillSummary{}, fmt.Errorf("%w: 汇总成交要且只要给执行账户或组合账户之一", ErrInvalidRecord)
	}
	end := query.EndTime
	if end <= 0 {
		end = 1<<62 - 1
	}
	var rows *sql.Rows
	var err error
	if query.TradingAccountID != "" {
		rows, err = s.db.WithContext(ctx).Raw(sqlSummarizeFillsByAccount, spaceID, query.TradingAccountID, query.StartTime, end).Rows()
	} else {
		rows, err = s.db.WithContext(ctx).Raw(sqlSummarizeFillsByLogical, spaceID, query.LogicalAccountID, query.StartTime, end).Rows()
	}
	if err != nil {
		return FillSummary{}, err
	}
	defer rows.Close()
	fees, pnls := map[string]shared.Decimal{}, map[string]shared.Decimal{}
	var count int64
	for rows.Next() {
		var fee, feeAsset, settlementAsset, realized string
		if err := rows.Scan(&fee, &feeAsset, &settlementAsset, &realized); err != nil {
			return FillSummary{}, err
		}
		feeValue, err := shared.ParseDecimal(fee)
		if err != nil {
			return FillSummary{}, fmt.Errorf("成交手续费 %q 不是合法数字：%w", fee, err)
		}
		pnlValue, err := shared.ParseDecimal(realized)
		if err != nil {
			return FillSummary{}, fmt.Errorf("成交已实现盈亏 %q 不是合法数字：%w", realized, err)
		}
		count++
		if !feeValue.IsZero() {
			fees[feeAsset] = fees[feeAsset].Add(feeValue)
		}
		if !pnlValue.IsZero() {
			pnls[settlementAsset] = pnls[settlementAsset].Add(pnlValue)
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
