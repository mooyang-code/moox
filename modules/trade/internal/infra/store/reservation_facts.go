package store

import (
	"fmt"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

// AvailableReducibleQuantity 返回账户在该标的上还能再挂只减仓单的数量：净持仓绝对值，减去同向只减仓挂单的未成交量。
// 现货没有独立持仓（持仓来自余额），返回 0。
func (tx *Tx) AvailableReducibleQuantity(account TradingAccountRecord, instrument InstrumentRecord) (shared.Decimal, error) {
	if exchange.RuleFor(instrument.MarketType).PositionFromBalance {
		return shared.Zero(), nil
	}
	var row struct {
		Quantity string `gorm:"column:c_signed_quantity"`
	}
	if err := tx.db.Raw(queryNetPositionQuantity, account.SpaceID, account.TradingAccountID, instrument.MarketType, instrument.ExchangeSymbol).Scan(&row).Error; err != nil {
		return shared.Zero(), err
	}
	signed := shared.Zero()
	if row.Quantity != "" {
		signed, _ = shared.ParseDecimal(row.Quantity)
	}
	available := signed.Abs()
	var rows []struct {
		Quantity string `gorm:"column:c_quantity"`
		Filled   string `gorm:"column:c_filled_quantity"`
		Side     string `gorm:"column:c_side"`
	}
	if err := tx.db.Raw(queryActiveReduceOnlyOrders, account.SpaceID, account.TradingAccountID, instrument.MarketType, instrument.ExchangeSymbol, activeOrderStatesJSON).Scan(&rows).Error; err != nil {
		return shared.Zero(), err
	}
	for _, row := range rows {
		// 只有平当前净持仓方向的只减仓单才占用额度；反方向的只减仓单对这个持仓不是有效预留。
		if (signed.Cmp(shared.Zero()) > 0 && row.Side != "SELL") ||
			(signed.Cmp(shared.Zero()) < 0 && row.Side != "BUY") {
			continue
		}
		q, e1 := shared.ParseDecimal(row.Quantity)
		f, e2 := shared.ParseDecimal(row.Filled)
		if e1 != nil || e2 != nil {
			return shared.Zero(), fmt.Errorf("%w: reduce-only facts", ErrInvalidRecord)
		}
		available = available.Sub(q.Sub(f))
	}
	if available.IsNegative() {
		return shared.Zero(), nil
	}
	return available, nil
}

// 以下 SQL 整条写成常量，不在调用处拼接或内联。
const (
	queryNetPositionQuantity = `SELECT c_signed_quantity FROM t_trading_positions
		WHERE c_space_id = ? AND c_trading_account_id = ? AND c_market_type = ? AND c_exchange_symbol = ? AND c_position_side = 'NET'`
	// 订单状态列表经 json_each(?) 传入（activeOrderStatesJSON）。
	queryActiveReduceOnlyOrders = `SELECT c_quantity, c_filled_quantity, c_side FROM t_trade_orders
		WHERE c_space_id = ? AND c_trading_account_id = ? AND c_market_type = ? AND c_exchange_symbol = ?
			AND c_reduce_only = 1 AND c_state IN (SELECT value FROM json_each(?))`
)
