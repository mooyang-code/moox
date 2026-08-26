package store

import (
	"context"
	"fmt"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

func (s *Store) ListPaperMatchCandidates(ctx context.Context, limit int) ([]OrderRecord, error) {
	if limit <= 0 {
		limit = 100000
	}
	var rows []orderRow
	err := s.db.WithContext(ctx).Table("t_trade_orders AS o").Joins("JOIN t_trading_accounts AS a ON a.c_space_id=o.c_space_id AND a.c_trading_account_id=o.c_trading_account_id").Where("a.c_execution_mode='PAPER' AND a.c_status='ENABLED' AND o.c_state='OPEN'").Order("o.c_first_match_pending DESC, o.c_mtime, o.c_order_id").Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	result := make([]OrderRecord, 0, len(rows))
	for _, row := range rows {
		result = append(result, orderRecordFromRow(row))
	}
	return result, nil
}

func (tx *Tx) GetOpenOrderForMatch(spaceID, orderID string, expectedVersion uint64) (OrderRecord, error) {
	var row orderRow
	result := tx.db.Table("t_trade_orders").Where(`
		c_space_id=? AND c_order_id=? AND c_state='OPEN' AND c_version=?
		AND EXISTS (
			SELECT 1 FROM t_trading_accounts a
			WHERE a.c_space_id=t_trade_orders.c_space_id
			  AND a.c_trading_account_id=t_trade_orders.c_trading_account_id
			  AND a.c_execution_mode='PAPER' AND a.c_status='ENABLED'
		)`, spaceID, orderID, expectedVersion).Take(&row)
	if result.Error != nil {
		return OrderRecord{}, result.Error
	}
	return orderRecordFromRow(row), nil
}

func (tx *Tx) ClearFirstMatchPending(current OrderRecord, expectedVersion uint64) error {
	r := tx.db.Exec("UPDATE t_trade_orders SET c_first_match_pending=0, c_version=c_version+1, c_mtime=CURRENT_TIMESTAMP WHERE c_space_id=? AND c_order_id=? AND c_version=? AND c_state='OPEN'", current.SpaceID, current.OrderID, expectedVersion)
	if r.Error != nil {
		return writeError(r.Error)
	}
	if r.RowsAffected != 1 {
		return fmt.Errorf("%w: stale paper order", ErrConflict)
	}
	return nil
}

func (tx *Tx) CancelPaperOrder(current OrderRecord, expectedVersion uint64, reason string) error {
	r := tx.db.Exec("UPDATE t_trade_orders SET c_state='CANCELED', c_first_match_pending=0, c_remaining_reserved_quantity='0', c_reject_reason=?, c_finished_at=CAST(strftime('%s','now') AS INTEGER)*1000, c_version=c_version+1, c_mtime=CURRENT_TIMESTAMP WHERE c_space_id=? AND c_order_id=? AND c_state='OPEN' AND c_version=?", reason, current.SpaceID, current.OrderID, expectedVersion)
	if r.Error != nil {
		return writeError(r.Error)
	}
	if r.RowsAffected != 1 {
		return fmt.Errorf("%w: stale paper order", ErrConflict)
	}
	return nil
}

func (tx *Tx) CanFillReduceOnly(order OrderRecord) (bool, error) {
	position, found, err := tx.GetPosition(order.SpaceID, order.TradingAccountID, order.ExchangeSymbol, order.PositionSide)
	if err != nil || !found {
		return false, err
	}
	qty, err := parseDecimal(order.Quantity)
	if err != nil {
		return false, err
	}
	filled, err := parseDecimal(order.FilledQuantity)
	if err != nil {
		return false, err
	}
	remaining := qty.Sub(filled)
	positionQty, err := shared.ParseDecimal(position.SignedQuantity)
	if err != nil {
		return false, err
	}
	if positionQty.IsZero() {
		return false, nil
	}
	// A reduce-only order is directional: SELL can only reduce a long and
	// BUY can only reduce a short. Re-check this at match time because another
	// fill may have changed the signed position since placement.
	if (positionQty.Cmp(shared.Zero()) > 0 && order.Side != "SELL") ||
		(positionQty.Cmp(shared.Zero()) < 0 && order.Side != "BUY") {
		return false, nil
	}
	return absDecimal(position.SignedQuantity).Cmp(remaining) >= 0, nil
}

func parseDecimal(raw string) (shared.Decimal, error) { return shared.ParseDecimal(raw) }
func absDecimal(raw string) shared.Decimal {
	v, err := shared.ParseDecimal(raw)
	if err != nil {
		return shared.Zero()
	}
	return v.Abs()
}
