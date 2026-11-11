package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

// GetUnreflectedReservation returns local order reservations that are newer
// than, or not yet represented by, the latest confirmed Exchange snapshot.
// markets 限定占用同一个钱包的市场：不同钱包的资金互不占用。
func (tx *Tx) GetUnreflectedReservation(
	spaceID string,
	tradingAccountID string,
	asset string,
	markets []string,
	snapshotSourceTime int64,
) (shared.Decimal, error) {
	marketsJSON, err := json.Marshal(markets)
	if err != nil {
		return shared.Decimal{}, fmt.Errorf("%w: encode reservation markets", ErrInvalidRecord)
	}
	// “还没提交完的”和“快照之后提交的”分两条查询各走各的索引：写成一条 OR 会扫账户在这些市场的全部订单，写成 UNION 会走主键；
	// 同时满足两个条件的订单按单号只算一次。
	var inFlight, submitted []struct {
		OrderID  string `gorm:"column:c_order_id"`
		Quantity string `gorm:"column:c_remaining_reserved_quantity"`
	}
	if err := tx.db.Raw(queryInFlightReservations, spaceID, tradingAccountID, asset, string(marketsJSON)).Scan(&inFlight).Error; err != nil {
		return shared.Decimal{}, err
	}
	if err := tx.db.Raw(querySubmittedReservationsSince, spaceID, tradingAccountID, snapshotSourceTime, asset, string(marketsJSON)).Scan(&submitted).Error; err != nil {
		return shared.Decimal{}, err
	}
	total := shared.Zero()
	seen := make(map[string]struct{}, len(inFlight)+len(submitted))
	for _, row := range append(inFlight, submitted...) {
		if _, dup := seen[row.OrderID]; dup {
			continue
		}
		seen[row.OrderID] = struct{}{}
		quantity, err := shared.ParseDecimal(row.Quantity)
		if err != nil || quantity.IsNegative() {
			return shared.Decimal{}, fmt.Errorf(
				"%w: stored unreflected reservation",
				ErrInvalidRecord,
			)
		}
		total = total.Add(quantity)
	}
	return total, nil
}

// RaiseObservedFilledQuantity 记录交易所订单更新里观察到的累计成交量（只升不降）。
func (tx *Tx) RaiseObservedFilledQuantity(spaceID, orderID string, observed shared.Decimal) error {
	var row struct {
		Observed string `gorm:"column:c_observed_filled_quantity"`
	}
	result := tx.db.Raw(queryObservedFilledQuantity, spaceID, orderID).Scan(&row)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("%w: missing order for observed fill quantity", ErrInvalidRecord)
	}
	current, err := shared.ParseDecimal(row.Observed)
	if err != nil || current.IsNegative() {
		return fmt.Errorf("%w: stored observed fill quantity", ErrInvalidRecord)
	}
	if observed.Cmp(current) <= 0 {
		return nil
	}
	return tx.db.Exec(execRaiseObservedFilledQuantity, observed.String(), spaceID, orderID).Error
}

// CountOrdersWithFillGap 返回账户下“观察到的累计成交量大于本地已入账成交量”的订单数：这些订单的成交明细还没补齐。
func (s *Store) CountOrdersWithFillGap(ctx context.Context, spaceID, tradingAccountID string) (int, error) {
	keys, err := s.OrderSymbolsWithFillGap(ctx, spaceID, tradingAccountID)
	return len(keys), err
}

// OrderSymbolsWithFillGap 返回累计成交仍领先已入账成交明细的订单的标的，每个订单一项。
func (s *Store) OrderSymbolsWithFillGap(ctx context.Context, spaceID, tradingAccountID string) ([]exchange.SymbolKey, error) {
	var rows []struct {
		Market   string `gorm:"column:c_market_type"`
		Symbol   string `gorm:"column:c_exchange_symbol"`
		Filled   string `gorm:"column:c_filled_quantity"`
		Observed string `gorm:"column:c_observed_filled_quantity"`
	}
	if err := s.db.WithContext(ctx).Raw(queryOrdersWithFillGap, spaceID, tradingAccountID).Scan(&rows).Error; err != nil {
		return nil, err
	}
	var keys []exchange.SymbolKey
	for _, row := range rows {
		filled, err := shared.ParseDecimal(row.Filled)
		if err != nil {
			return nil, fmt.Errorf("%w: stored filled quantity", ErrInvalidRecord)
		}
		observed, err := shared.ParseDecimal(row.Observed)
		if err != nil {
			return nil, fmt.Errorf("%w: stored observed fill quantity", ErrInvalidRecord)
		}
		if observed.Cmp(filled) > 0 {
			keys = append(keys, exchange.NewSymbolKey(row.Market, row.Symbol))
		}
	}
	return keys, nil
}

// SettleFillGapsOfGoneInstrument 把账户在已下架标的上“观察累计成交量大于已入账成交量”的订单（含已结束的单）的
// 观察成交量压回已入账值，返回被压回的单号。标的下架后交易所不再给成交明细，缺口永远补不上；不压回的话账户
// 每次同步都判有缺口、一直未就绪。持仓、余额照常以交易所同步结果为准。
func (tx *Tx) SettleFillGapsOfGoneInstrument(spaceID, tradingAccountID string, key exchange.SymbolKey) ([]string, error) {
	var rows []struct {
		OrderID  string `gorm:"column:c_order_id"`
		Filled   string `gorm:"column:c_filled_quantity"`
		Observed string `gorm:"column:c_observed_filled_quantity"`
	}
	if err := tx.db.Raw(
		queryInstrumentOrdersWithFillGap, spaceID, tradingAccountID, string(key.Market), key.Symbol,
	).Scan(&rows).Error; err != nil {
		return nil, err
	}
	var settled []string
	for _, row := range rows {
		filled, err := shared.ParseDecimal(row.Filled)
		if err != nil {
			return nil, fmt.Errorf("%w: stored filled quantity", ErrInvalidRecord)
		}
		observed, err := shared.ParseDecimal(row.Observed)
		if err != nil {
			return nil, fmt.Errorf("%w: stored observed fill quantity", ErrInvalidRecord)
		}
		if observed.Cmp(filled) <= 0 {
			continue
		}
		settled = append(settled, row.OrderID)
	}
	// 查询不带 ORDER BY（带了会走主键扫整个空间），按单号排好再逐笔压回。
	sort.Strings(settled)
	for _, orderID := range settled {
		if err := tx.db.Exec(updateObservedFilledToFilled, spaceID, orderID).Error; err != nil {
			return nil, err
		}
	}
	return settled, nil
}

// 以下 SQL 整条写成常量，不在调用处拼接或内联。
const (
	// 小数不能按字符串比大小：这里只筛出候选，Go 里再比。不带 ORDER BY：按单号排序会让规划器走主键扫整个空间，
	// 调用方在 Go 里排。
	queryInstrumentOrdersWithFillGap = `SELECT c_order_id, c_filled_quantity, c_observed_filled_quantity
		FROM t_trade_orders
		WHERE c_space_id = ? AND c_trading_account_id = ? AND c_market_type = ? AND c_exchange_symbol = ?
			AND c_observed_filled_quantity != '0'
			AND c_observed_filled_quantity <> c_filled_quantity`
	updateObservedFilledToFilled = `UPDATE t_trade_orders SET c_observed_filled_quantity = c_filled_quantity
		WHERE c_space_id = ? AND c_order_id = ?`
	// 以下两条是未反映预留的两半，市场列表经 json_each(?) 传入。c_market_type 前面的 + 让规划器不改选按标的的索引：
	// 第一条走 idx_trade_orders_account_state，第二条走 idx_trade_orders_account_submitted。
	queryInFlightReservations = `SELECT c_order_id, c_remaining_reserved_quantity
		FROM t_trade_orders
		WHERE c_space_id = ? AND c_trading_account_id = ?
			AND c_state IN ('PENDING', 'SUBMITTING', 'SUBMIT_UNKNOWN')
			AND c_reserved_asset = ?
			AND +c_market_type IN (SELECT value FROM json_each(?))
			AND c_remaining_reserved_quantity != '0'`
	querySubmittedReservationsSince = `SELECT c_order_id, c_remaining_reserved_quantity
		FROM t_trade_orders
		WHERE c_space_id = ? AND c_trading_account_id = ?
			AND c_submitted_at >= ?
			AND c_reserved_asset = ?
			AND +c_market_type IN (SELECT value FROM json_each(?))
			AND c_remaining_reserved_quantity != '0'`
	// 条件与部分索引 idx_trade_orders_fill_gap 的 WHERE 逐项一致，规划器才会用它。
	queryOrdersWithFillGap = `SELECT c_market_type, c_exchange_symbol, c_filled_quantity, c_observed_filled_quantity
		FROM t_trade_orders
		WHERE c_space_id = ? AND c_trading_account_id = ? AND c_observed_filled_quantity != '0'
			AND c_observed_filled_quantity <> c_filled_quantity`
	queryObservedFilledQuantity = `SELECT c_observed_filled_quantity FROM t_trade_orders
		WHERE c_space_id = ? AND c_order_id = ?`
	execRaiseObservedFilledQuantity = `UPDATE t_trade_orders SET c_observed_filled_quantity = ?
		WHERE c_space_id = ? AND c_order_id = ?`
)
