package store

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

// GetUnreflectedReservation returns local order reservations that are newer
// than, or not yet represented by, the latest confirmed Exchange snapshot.
func (tx *Tx) GetUnreflectedReservation(
	spaceID string,
	tradingAccountID string,
	asset string,
	snapshotSourceTime int64,
) (shared.Decimal, error) {
	var rows []struct {
		Quantity string `gorm:"column:c_remaining_reserved_quantity"`
	}
	if err := tx.db.Raw(`
		SELECT c_remaining_reserved_quantity
		FROM t_trade_orders
		WHERE c_space_id = ? AND c_trading_account_id = ?
			AND c_reserved_asset = ?
			AND c_remaining_reserved_quantity != '0'
			AND (
				c_state IN ('PENDING', 'SUBMITTING', 'SUBMIT_UNKNOWN')
				OR c_submitted_at >= ?
			)
	`, spaceID, tradingAccountID, asset, snapshotSourceTime).Scan(&rows).Error; err != nil {
		return shared.Decimal{}, err
	}
	total := shared.Zero()
	for _, row := range rows {
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
	result := tx.db.Raw(
		"SELECT c_observed_filled_quantity FROM t_trade_orders WHERE c_space_id = ? AND c_order_id = ?",
		spaceID, orderID,
	).Scan(&row)
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
	return tx.db.Exec(
		"UPDATE t_trade_orders SET c_observed_filled_quantity = ? WHERE c_space_id = ? AND c_order_id = ?",
		observed.String(), spaceID, orderID,
	).Error
}

// CountOrdersWithFillGap 返回账户下“观察到的累计成交量大于本地已入账成交量”的订单数：这些订单的成交明细还没补齐。
func (s *Store) CountOrdersWithFillGap(ctx context.Context, spaceID, tradingAccountID string) (int, error) {
	var rows []struct {
		Filled   string `gorm:"column:c_filled_quantity"`
		Observed string `gorm:"column:c_observed_filled_quantity"`
	}
	if err := s.db.WithContext(ctx).Raw(`
		SELECT c_filled_quantity, c_observed_filled_quantity
		FROM t_trade_orders
		WHERE c_space_id = ? AND c_trading_account_id = ? AND c_observed_filled_quantity != '0'
	`, spaceID, tradingAccountID).Scan(&rows).Error; err != nil {
		return 0, err
	}
	gaps := 0
	for _, row := range rows {
		filled, err := shared.ParseDecimal(row.Filled)
		if err != nil {
			return 0, fmt.Errorf("%w: stored filled quantity", ErrInvalidRecord)
		}
		observed, err := shared.ParseDecimal(row.Observed)
		if err != nil {
			return 0, fmt.Errorf("%w: stored observed fill quantity", ErrInvalidRecord)
		}
		if observed.Cmp(filled) > 0 {
			gaps++
		}
	}
	return gaps, nil
}
