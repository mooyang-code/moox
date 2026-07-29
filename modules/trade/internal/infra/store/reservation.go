package store

import (
	"fmt"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

// GetUnreflectedReservation returns local order reservations that are newer
// than, or not yet represented by, the latest confirmed Exchange snapshot.
func (tx *Tx) GetUnreflectedReservation(
	spaceID string,
	exchangeAccountID string,
	asset string,
	snapshotSourceTime int64,
) (shared.Decimal, error) {
	var rows []struct {
		Quantity string `gorm:"column:c_remaining_reserved_quantity"`
	}
	if err := tx.db.Raw(`
		SELECT c_remaining_reserved_quantity
		FROM t_trade_orders
		WHERE c_space_id = ? AND c_exchange_account_id = ?
			AND c_reserved_asset = ?
			AND c_remaining_reserved_quantity != '0'
			AND (
				c_state IN ('PENDING', 'SUBMITTING', 'SUBMIT_UNKNOWN')
				OR c_submitted_at > ?
			)
	`, spaceID, exchangeAccountID, asset, snapshotSourceTime).Scan(&rows).Error; err != nil {
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
