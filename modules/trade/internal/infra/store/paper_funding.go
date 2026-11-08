package store

import (
	"context"
	"fmt"
	"sort"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

// PaperFundingFeeRecord 是模拟盘一个合约在一个结算时刻的资金费。
type PaperFundingFeeRecord struct {
	SpaceID          string
	TradingAccountID string
	ExchangeSymbol   string
	// SettledAt 是交易所公布的结算时刻（毫秒），原样保存。
	SettledAt   int64
	FundingRate string
	MarkPrice   string
	// Quantity 是结算时刻的持仓数量，多为正、空为负。
	Quantity string
	// Amount 是记入余额的金额，收到为正、支付为负。
	Amount string
	Asset  string
}

// PaperSwapFill 是一笔永续成交对持仓的带符号变动（买为正、卖为负）。
type PaperSwapFill struct {
	ExchangeSymbol string
	TradedAt       int64
	Delta          shared.Decimal
}

// PaperSwapActivity 是推算结算时刻持仓用的数据：当前的永续持仓，以及某个时刻起（含）的永续成交，按时间升序。
// 模拟盘的持仓只由本地成交投影（重置时成交和持仓一起清），所以任一时刻的持仓都能由当前持仓减去之后的成交推出来。
type PaperSwapActivity struct {
	Positions map[string]shared.Decimal
	Fills     []PaperSwapFill
}

// Sweep 返回从读取起点起按时间向前推进的持仓：先由当前持仓减去起点之后的全部成交得到起点时刻的持仓，再逐笔加回。
func (a PaperSwapActivity) Sweep() *PaperPositionSweep {
	positions := make(map[string]shared.Decimal, len(a.Positions))
	for symbol, quantity := range a.Positions {
		positions[symbol] = quantity
	}
	for _, fill := range a.Fills {
		positions[fill.ExchangeSymbol] = positions[fill.ExchangeSymbol].Sub(fill.Delta)
	}
	return &PaperPositionSweep{positions: positions, fills: a.Fills}
}

// Symbols 返回当前有持仓或读取起点之后有成交的合约：只有它们可能在起点之后的结算点上有持仓。
func (a PaperSwapActivity) Symbols() map[string]struct{} {
	symbols := make(map[string]struct{}, len(a.Positions)+len(a.Fills))
	for symbol, quantity := range a.Positions {
		if !quantity.IsZero() {
			symbols[symbol] = struct{}{}
		}
	}
	for _, fill := range a.Fills {
		symbols[fill.ExchangeSymbol] = struct{}{}
	}
	return symbols
}

// PaperPositionSweep 按时间向前推进的永续持仓，只能前进，不能后退。
type PaperPositionSweep struct {
	positions map[string]shared.Decimal
	fills     []PaperSwapFill
	next      int
}

// AdvanceTo 把持仓推进到结算时刻 at：严格早于 at 的成交算在持仓里，at 及之后的不算。
func (s *PaperPositionSweep) AdvanceTo(at int64) {
	for s.next < len(s.fills) && s.fills[s.next].TradedAt < at {
		fill := s.fills[s.next]
		s.positions[fill.ExchangeSymbol] = s.positions[fill.ExchangeSymbol].Add(fill.Delta)
		s.next++
	}
}

// Quantity 返回当前推进到的时刻某个合约的持仓，多为正、空为负。
func (s *PaperPositionSweep) Quantity(symbol string) shared.Decimal {
	return s.positions[symbol]
}

// ExposedUntil 报告从当前推进到的时刻到 to（含）之间是否有持仓不为零：现在就有持仓，或这段时间里有成交。
func (s *PaperPositionSweep) ExposedUntil(to int64) bool {
	for _, quantity := range s.positions {
		if !quantity.IsZero() {
			return true
		}
	}
	return s.next < len(s.fills) && s.fills[s.next].TradedAt <= to
}

// paperFundingAmount 是一次结算记入余额的金额：-持仓数量 × 标记价格 × 费率。费率为正时多头支付、空头收到。
func paperFundingAmount(quantity, markPrice, rate shared.Decimal) shared.Decimal {
	return quantity.Mul(markPrice).Mul(rate).Neg()
}

// LatestPaperFundingSettlement 返回账户最后结算到的时刻（表里最大的结算时刻，毫秒）；还没有记录时为 0。
func (s *Store) LatestPaperFundingSettlement(ctx context.Context, spaceID, tradingAccountID string) (int64, error) {
	var latest int64
	err := s.db.WithContext(ctx).Raw(queryLatestPaperFundingSettlement, spaceID, tradingAccountID).Scan(&latest).Error
	return latest, err
}

// PaperSwapActivity 读当前永续持仓和 since 起（含）的永续成交。
func (s *Store) PaperSwapActivity(ctx context.Context, spaceID, tradingAccountID string, since int64) (PaperSwapActivity, error) {
	var result PaperSwapActivity
	err := s.Transaction(ctx, func(tx *Tx) error {
		var err error
		result, err = tx.paperSwapActivity(spaceID, tradingAccountID, since)
		return err
	})
	return result, err
}

func (tx *Tx) paperSwapActivity(spaceID, tradingAccountID string, since int64) (PaperSwapActivity, error) {
	result := PaperSwapActivity{Positions: map[string]shared.Decimal{}}
	var positions []struct {
		Symbol   string `gorm:"column:c_exchange_symbol"`
		Quantity string `gorm:"column:c_signed_quantity"`
	}
	if err := tx.db.Raw(queryPaperSwapPositions, spaceID, tradingAccountID, string(exchange.MarketTypeSwap)).Scan(&positions).Error; err != nil {
		return result, err
	}
	for _, position := range positions {
		quantity, err := shared.ParseDecimal(position.Quantity)
		if err != nil {
			return result, fmt.Errorf("%w: paper position quantity", ErrInvalidRecord)
		}
		result.Positions[position.Symbol] = result.Positions[position.Symbol].Add(quantity)
	}
	var fills []struct {
		Symbol   string `gorm:"column:c_exchange_symbol"`
		Side     string `gorm:"column:c_side"`
		Quantity string `gorm:"column:c_quantity"`
		TradedAt int64  `gorm:"column:c_traded_at"`
	}
	if err := tx.db.Raw(queryPaperSwapFillsSince, spaceID, tradingAccountID, string(exchange.MarketTypeSwap), since).Scan(&fills).Error; err != nil {
		return result, err
	}
	for _, fill := range fills {
		quantity, err := shared.ParseDecimal(fill.Quantity)
		if err != nil {
			return result, fmt.Errorf("%w: paper fill quantity", ErrInvalidRecord)
		}
		switch fill.Side {
		case string(exchange.SideBuy):
		case string(exchange.SideSell):
			quantity = quantity.Neg()
		default:
			return result, fmt.Errorf("%w: paper fill side", ErrInvalidRecord)
		}
		result.Fills = append(result.Fills, PaperSwapFill{ExchangeSymbol: fill.Symbol, TradedAt: fill.TradedAt, Delta: quantity})
	}
	return result, nil
}

// SettlePaperFunding 在一个事务里结算一批资金费：按结算时刻的持仓算金额，插入记录并改结算资产余额。
// 唯一键（账户、合约、结算时刻）保证幂等：已经结算过的记录不再插入，也不再改余额。持仓为零、标记价格缺失的记录跳过。
// 余额改动按资产累加，事务末尾每个资产只写一次，事务里不逐条读改写余额。返回本次新插入的记录。
func (s *Store) SettlePaperFunding(ctx context.Context, spaceID, tradingAccountID string, rates []exchange.FundingRate) ([]PaperFundingFeeRecord, error) {
	if len(rates) == 0 {
		return nil, nil
	}
	ordered := append([]exchange.FundingRate(nil), rates...)
	sort.Slice(ordered, func(i, j int) bool {
		if !ordered[i].FundingTime.Equal(ordered[j].FundingTime) {
			return ordered[i].FundingTime.Before(ordered[j].FundingTime)
		}
		return ordered[i].ExchangeSymbol < ordered[j].ExchangeSymbol
	})
	var inserted []PaperFundingFeeRecord
	err := s.Transaction(ctx, func(tx *Tx) error {
		inserted = nil
		account, err := tx.GetTradingAccount(spaceID, tradingAccountID)
		if err != nil {
			return err
		}
		if account.ExecutionMode != string(exchange.ExecutionModePaper) || account.SettlementAsset == "" {
			return fmt.Errorf("%w: paper funding account", ErrInvalidRecord)
		}
		// 重置运行数据后余额投影要按初始资金重建，先确认它存在。
		if _, err := tx.GetPaperBalanceSnapshot(spaceID, tradingAccountID); err != nil {
			return err
		}
		// 持仓在事务里读：和余额改动看到的是同一份成交。
		activity, err := tx.paperSwapActivity(spaceID, tradingAccountID, ordered[0].FundingTime.UnixMilli())
		if err != nil {
			return err
		}
		positions := activity.Sweep()
		deltas := map[string]shared.Decimal{}
		for _, rate := range ordered {
			settledAt := rate.FundingTime.UnixMilli()
			if rate.ExchangeSymbol == "" || settledAt <= 0 || rate.MarkPrice.Cmp(shared.Zero()) <= 0 {
				continue
			}
			positions.AdvanceTo(settledAt)
			quantity := positions.Quantity(rate.ExchangeSymbol)
			if quantity.IsZero() {
				continue
			}
			amount := paperFundingAmount(quantity, rate.MarkPrice, rate.Rate)
			record := PaperFundingFeeRecord{
				SpaceID: spaceID, TradingAccountID: tradingAccountID, ExchangeSymbol: rate.ExchangeSymbol, SettledAt: settledAt,
				FundingRate: rate.Rate.String(), MarkPrice: rate.MarkPrice.String(), Quantity: quantity.String(),
				Amount: amount.String(), Asset: account.SettlementAsset,
			}
			result := tx.db.Exec(execInsertPaperFundingFee, record.SpaceID, record.TradingAccountID, record.ExchangeSymbol, record.SettledAt,
				record.FundingRate, record.MarkPrice, record.Quantity, record.Amount, record.Asset)
			if result.Error != nil {
				return writeError(result.Error)
			}
			if result.RowsAffected == 0 {
				continue
			}
			deltas[record.Asset] = deltas[record.Asset].Add(amount)
			inserted = append(inserted, record)
		}
		for asset, delta := range deltas {
			if err := tx.addPaperBalance(spaceID, tradingAccountID, asset, delta); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return inserted, nil
}

// ListPaperFundingFees 返回 [start, end] 内最新的至多 limit 条资金费记录（按结算时刻倒序）和区间内的总条数。
func (s *Store) ListPaperFundingFees(ctx context.Context, spaceID, tradingAccountID string, start, end int64, limit int) ([]PaperFundingFeeRecord, int64, error) {
	var total int64
	if err := s.db.WithContext(ctx).Raw(queryCountPaperFundingFees, spaceID, tradingAccountID, start, end).Scan(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []struct {
		SpaceID          string `gorm:"column:c_space_id"`
		TradingAccountID string `gorm:"column:c_trading_account_id"`
		ExchangeSymbol   string `gorm:"column:c_exchange_symbol"`
		SettledAt        int64  `gorm:"column:c_settled_at"`
		FundingRate      string `gorm:"column:c_funding_rate"`
		MarkPrice        string `gorm:"column:c_mark_price"`
		Quantity         string `gorm:"column:c_quantity"`
		Amount           string `gorm:"column:c_amount"`
		Asset            string `gorm:"column:c_asset"`
	}
	if err := s.db.WithContext(ctx).Raw(queryListPaperFundingFees, spaceID, tradingAccountID, start, end, limit).Scan(&rows).Error; err != nil {
		return nil, 0, err
	}
	records := make([]PaperFundingFeeRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, PaperFundingFeeRecord{
			SpaceID: row.SpaceID, TradingAccountID: row.TradingAccountID, ExchangeSymbol: row.ExchangeSymbol, SettledAt: row.SettledAt,
			FundingRate: row.FundingRate, MarkPrice: row.MarkPrice, Quantity: row.Quantity, Amount: row.Amount, Asset: row.Asset,
		})
	}
	return records, total, nil
}

// SummarizePaperFundingFees 按资产汇总 [start, end] 内的资金费，逐行用十进制累加（金额是字符串，不能让数据库按浮点求和），按资产排序。
// 收益拆解直接用它：不经过流水翻页，记录再多也不截断。
func (s *Store) SummarizePaperFundingFees(ctx context.Context, spaceID, tradingAccountID string, start, end int64) ([]AssetAmount, error) {
	rows, err := s.db.WithContext(ctx).Raw(querySummarizePaperFundingFees, spaceID, tradingAccountID, start, end).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	totals := map[string]shared.Decimal{}
	for rows.Next() {
		var asset, raw string
		if err := rows.Scan(&asset, &raw); err != nil {
			return nil, err
		}
		amount, err := shared.ParseDecimal(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: paper funding amount", ErrInvalidRecord)
		}
		totals[asset] = totals[asset].Add(amount)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := make([]AssetAmount, 0, len(totals))
	for asset, amount := range totals {
		result = append(result, AssetAmount{Asset: asset, Amount: amount})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Asset < result[j].Asset })
	return result, nil
}

// addPaperBalance 把一笔变动加到模拟账户某个资产的余额上；资产还没有余额行时从零开始。
func (tx *Tx) addPaperBalance(spaceID, tradingAccountID, asset string, delta shared.Decimal) error {
	var row struct {
		Total string `gorm:"column:c_total"`
	}
	result := tx.db.Raw(queryPaperAssetBalance, spaceID, tradingAccountID, asset).Scan(&row)
	if result.Error != nil {
		return result.Error
	}
	total := shared.Zero()
	if result.RowsAffected != 0 {
		var err error
		total, err = shared.ParseDecimal(row.Total)
		if err != nil {
			return fmt.Errorf("%w: stored paper balance", ErrInvalidRecord)
		}
	}
	next := total.Add(delta).String()
	if _, err := shared.ParseDecimal(next); err != nil {
		return fmt.Errorf("%w: paper balance precision", ErrInvalidRecord)
	}
	return tx.db.Exec(execUpsertPaperAssetBalance, spaceID, tradingAccountID, asset, next).Error
}

// 以下 SQL 整条写成常量，不在调用处拼接或内联。
const (
	queryLatestPaperFundingSettlement = `SELECT COALESCE(MAX(c_settled_at), 0) FROM t_paper_funding_fees
		WHERE c_space_id = ? AND c_trading_account_id = ?`
	queryPaperSwapPositions = `SELECT c_exchange_symbol, c_signed_quantity FROM t_trading_positions
		WHERE c_space_id = ? AND c_trading_account_id = ? AND c_market_type = ?`
	queryPaperSwapFillsSince = `SELECT c_exchange_symbol, c_side, c_quantity, c_traded_at FROM t_order_fills
		WHERE c_space_id = ? AND c_trading_account_id = ? AND c_market_type = ? AND c_traded_at >= ?
		ORDER BY c_traded_at, c_fill_id`
	execInsertPaperFundingFee = `INSERT INTO t_paper_funding_fees (
			c_space_id, c_trading_account_id, c_exchange_symbol, c_settled_at,
			c_funding_rate, c_mark_price, c_quantity, c_amount, c_asset
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (c_space_id, c_trading_account_id, c_exchange_symbol, c_settled_at) DO NOTHING`
	queryCountPaperFundingFees = `SELECT COUNT(*) FROM t_paper_funding_fees
		WHERE c_space_id = ? AND c_trading_account_id = ? AND c_settled_at >= ? AND c_settled_at <= ?`
	queryListPaperFundingFees = `SELECT c_space_id, c_trading_account_id, c_exchange_symbol, c_settled_at,
			c_funding_rate, c_mark_price, c_quantity, c_amount, c_asset
		FROM t_paper_funding_fees
		WHERE c_space_id = ? AND c_trading_account_id = ? AND c_settled_at >= ? AND c_settled_at <= ?
		ORDER BY c_settled_at DESC, c_exchange_symbol DESC
		LIMIT ?`
	querySummarizePaperFundingFees = `SELECT c_asset, c_amount FROM t_paper_funding_fees
		WHERE c_space_id = ? AND c_trading_account_id = ? AND c_settled_at >= ? AND c_settled_at <= ?`
	queryPaperAssetBalance = `SELECT c_total FROM t_paper_asset_balances
		WHERE c_space_id = ? AND c_trading_account_id = ? AND c_asset = ?`
	execUpsertPaperAssetBalance = `INSERT INTO t_paper_asset_balances (c_space_id, c_trading_account_id, c_asset, c_total) VALUES (?, ?, ?, ?)
		ON CONFLICT (c_space_id, c_trading_account_id, c_asset) DO UPDATE SET c_total = excluded.c_total`
)
