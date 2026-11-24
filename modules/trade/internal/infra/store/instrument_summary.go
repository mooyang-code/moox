package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

// InstrumentSummaryRecord 是标的汇总表（t_account_instrument_summaries）的一行：持仓来自余额的市场里，
// 一个执行账户在一个标的上的成交累计。订单与成交按保留期清理之后，受管判断、持仓均价和取不到价时的最近成交价都读它。
type InstrumentSummaryRecord struct {
	SpaceID          string
	TradingAccountID string
	MarketType       string
	ExchangeSymbol   string
	InstrumentID     string
	// Managed 表示这个标的上有过 TARGET 或 OPERATOR 订单的成交。
	Managed bool
	// Quantity、Cost 是按每一笔成交累计的数量与成本（见 applyInstrumentSummary）。
	Quantity shared.Decimal
	Cost     shared.Decimal
	// LastPrice 是成交时间最晚（同一毫秒取成交号最大）的那笔成交的价格。
	LastPrice    shared.Decimal
	LastTradedAt int64
}

// AverageCost 是累计成本除以累计数量；数量为 0 时为 0。
func (r InstrumentSummaryRecord) AverageCost() shared.Decimal {
	if r.Quantity.IsZero() {
		return shared.Zero()
	}
	return r.Cost.Div(r.Quantity)
}

type instrumentSummaryRow struct {
	SpaceID          string `gorm:"column:c_space_id"`
	TradingAccountID string `gorm:"column:c_trading_account_id"`
	MarketType       string `gorm:"column:c_market_type"`
	ExchangeSymbol   string `gorm:"column:c_exchange_symbol"`
	InstrumentID     string `gorm:"column:c_instrument_id"`
	Managed          bool   `gorm:"column:c_managed"`
	Quantity         string `gorm:"column:c_quantity"`
	Cost             string `gorm:"column:c_cost"`
	LastPrice        string `gorm:"column:c_last_price"`
	LastTradedAt     int64  `gorm:"column:c_last_traded_at"`
	LastFillID       string `gorm:"column:c_last_fill_id"`
}

func (row instrumentSummaryRow) record() (InstrumentSummaryRecord, error) {
	quantity, err := shared.ParseDecimal(row.Quantity)
	if err != nil {
		return InstrumentSummaryRecord{}, fmt.Errorf("%w: 标的汇总的数量 %q", ErrInvalidRecord, row.Quantity)
	}
	cost, err := shared.ParseDecimal(row.Cost)
	if err != nil {
		return InstrumentSummaryRecord{}, fmt.Errorf("%w: 标的汇总的成本 %q", ErrInvalidRecord, row.Cost)
	}
	price, err := shared.ParseDecimal(row.LastPrice)
	if err != nil {
		return InstrumentSummaryRecord{}, fmt.Errorf("%w: 标的汇总的最近成交价 %q", ErrInvalidRecord, row.LastPrice)
	}
	return InstrumentSummaryRecord{
		SpaceID: row.SpaceID, TradingAccountID: row.TradingAccountID, MarketType: row.MarketType,
		ExchangeSymbol: row.ExchangeSymbol, InstrumentID: row.InstrumentID, Managed: row.Managed,
		Quantity: quantity, Cost: cost, LastPrice: price, LastTradedAt: row.LastTradedAt,
	}, nil
}

// applyInstrumentSummary 把一笔新入账的成交累加进标的汇总，必须在写成交的同一个事务里、只对真正新增的成交调用。
// 只记持仓来自余额的市场：合约的均价来自持仓表，不走这里。
//   - 受管：成交所属订单的来源是 TARGET 或 OPERATOR 时置位，之后不清；
//   - 数量与成本：每一笔成交都更新，不分订单来源。买入时数量与成本都累加；卖出时减少量不超过当前数量，成本按当时均价扣减；
//     数量为 0 时的卖出忽略；数量归零后成本归零；
//   - 最近成交价：成交时间更晚（同一毫秒成交号更大）的才替换。
//
// 数量与成本按入账顺序累计：模拟盘的成交按时间顺序产生，实盘成交乱序到达时与按成交时间重算可能有出入（见交易模块文档的已知差别）。
func (tx *Tx) applyInstrumentSummary(record FillRecord, ownerType string) error {
	if !exchange.RuleFor(record.MarketType).PositionFromBalance {
		return nil
	}
	quantity, quantityErr := shared.ParseDecimal(record.Quantity)
	price, priceErr := shared.ParseDecimal(record.Price)
	if quantityErr != nil || priceErr != nil {
		return fmt.Errorf("%w: 成交的数量或价格不是合法数字", ErrInvalidRecord)
	}
	var rows []instrumentSummaryRow
	if err := tx.db.Raw(queryInstrumentSummary, record.SpaceID, record.TradingAccountID, record.MarketType, record.ExchangeSymbol).
		Scan(&rows).Error; err != nil {
		return err
	}
	current := instrumentSummaryRow{
		SpaceID: record.SpaceID, TradingAccountID: record.TradingAccountID,
		MarketType: record.MarketType, ExchangeSymbol: record.ExchangeSymbol,
		Quantity: "0", Cost: "0", LastPrice: record.Price, LastTradedAt: record.TradedAt, LastFillID: record.FillID,
	}
	if len(rows) > 0 {
		current = rows[0]
		if record.TradedAt > current.LastTradedAt || (record.TradedAt == current.LastTradedAt && record.FillID > current.LastFillID) {
			current.LastPrice, current.LastTradedAt, current.LastFillID = record.Price, record.TradedAt, record.FillID
		}
	}
	if current.InstrumentID == "" {
		current.InstrumentID = record.InstrumentID
	}
	if ownerType == "TARGET" || ownerType == "OPERATOR" {
		current.Managed = true
	}
	held, err := shared.ParseDecimal(current.Quantity)
	if err != nil {
		return fmt.Errorf("%w: 标的汇总的数量 %q", ErrInvalidRecord, current.Quantity)
	}
	cost, err := shared.ParseDecimal(current.Cost)
	if err != nil {
		return fmt.Errorf("%w: 标的汇总的成本 %q", ErrInvalidRecord, current.Cost)
	}
	held, cost = accumulateHolding(held, cost, record.Side, quantity, price)
	current.Quantity, current.Cost = held.String(), cost.String()
	return writeError(tx.db.Exec(execUpsertInstrumentSummary,
		current.SpaceID, current.TradingAccountID, current.MarketType, current.ExchangeSymbol, current.InstrumentID,
		current.Managed, current.Quantity, current.Cost, current.LastPrice, current.LastTradedAt, current.LastFillID).Error)
}

// accumulateHolding 把一笔成交累加进（数量，成本），公式见 applyInstrumentSummary。
func accumulateHolding(held, cost shared.Decimal, side string, quantity, price shared.Decimal) (shared.Decimal, shared.Decimal) {
	if quantity.IsZero() {
		return held, cost
	}
	if strings.EqualFold(side, string(exchange.SideBuy)) {
		return held.Add(quantity), cost.Add(quantity.Mul(price))
	}
	if !strings.EqualFold(side, string(exchange.SideSell)) || held.IsZero() {
		return held, cost
	}
	reduce := quantity
	if reduce.Cmp(held) > 0 {
		reduce = held
	}
	average := cost.Div(held)
	held = held.Sub(reduce)
	if held.IsZero() {
		return held, shared.Zero()
	}
	return held, cost.Sub(average.Mul(reduce))
}

// GetInstrumentSummary 取执行账户一个标的的汇总；没有成交过时 found 为假。
func (s *Store) GetInstrumentSummary(ctx context.Context, spaceID, tradingAccountID string, key exchange.SymbolKey) (InstrumentSummaryRecord, bool, error) {
	var rows []instrumentSummaryRow
	if err := s.db.WithContext(ctx).Raw(queryInstrumentSummary, spaceID, tradingAccountID, string(key.Market), key.Symbol).
		Scan(&rows).Error; err != nil {
		return InstrumentSummaryRecord{}, false, err
	}
	if len(rows) == 0 {
		return InstrumentSummaryRecord{}, false, nil
	}
	record, err := rows[0].record()
	return record, err == nil, err
}

// ManagedSymbolKeys 返回执行账户上受管的标的（有过 TARGET 或 OPERATOR 订单的成交），按（市场，代码）。
func (s *Store) ManagedSymbolKeys(ctx context.Context, spaceID, tradingAccountID string) (map[exchange.SymbolKey]struct{}, error) {
	var rows []struct {
		MarketType     string `gorm:"column:c_market_type"`
		ExchangeSymbol string `gorm:"column:c_exchange_symbol"`
	}
	if err := s.db.WithContext(ctx).Raw(queryManagedSymbols, spaceID, tradingAccountID).Scan(&rows).Error; err != nil {
		return nil, err
	}
	keys := make(map[exchange.SymbolKey]struct{}, len(rows))
	for _, row := range rows {
		keys[exchange.NewSymbolKey(exchange.MarketType(row.MarketType), row.ExchangeSymbol)] = struct{}{}
	}
	return keys, nil
}

// 以下 SQL 整条写成常量，不在调用处拼接或内联。
const (
	queryInstrumentSummary = `SELECT c_space_id, c_trading_account_id, c_market_type, c_exchange_symbol, c_instrument_id,
			c_managed, c_quantity, c_cost, c_last_price, c_last_traded_at, c_last_fill_id
		FROM t_account_instrument_summaries
		WHERE c_space_id = ? AND c_trading_account_id = ? AND c_market_type = ? AND c_exchange_symbol = ?`
	queryManagedSymbols = `SELECT c_market_type, c_exchange_symbol
		FROM t_account_instrument_summaries
		WHERE c_space_id = ? AND c_trading_account_id = ? AND c_managed = 1`
	execUpsertInstrumentSummary = `INSERT INTO t_account_instrument_summaries (
			c_space_id, c_trading_account_id, c_market_type, c_exchange_symbol, c_instrument_id,
			c_managed, c_quantity, c_cost, c_last_price, c_last_traded_at, c_last_fill_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (c_space_id, c_trading_account_id, c_market_type, c_exchange_symbol) DO UPDATE SET
			c_instrument_id = excluded.c_instrument_id, c_managed = excluded.c_managed,
			c_quantity = excluded.c_quantity, c_cost = excluded.c_cost, c_last_price = excluded.c_last_price,
			c_last_traded_at = excluded.c_last_traded_at, c_last_fill_id = excluded.c_last_fill_id`
)

// SeedInstrumentSummaryForTest 只给测试用：直接写一行标的汇总，代替“建已成交订单再逐笔入账成交”，不动模拟余额。
func (tx *Tx) SeedInstrumentSummaryForTest(record InstrumentSummaryRecord) error {
	return writeError(tx.db.Exec(execUpsertInstrumentSummary,
		record.SpaceID, record.TradingAccountID, record.MarketType, record.ExchangeSymbol, record.InstrumentID,
		record.Managed, record.Quantity.String(), record.Cost.String(), record.LastPrice.String(), record.LastTradedAt, "seed").Error)
}
