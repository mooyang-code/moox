package store

import (
	"fmt"
)

// ClosePaperSimulation atomically stops a paper account and its single logical
// account membership. It is deliberately small: paper simulations are an
// immutable fixture, not a second account-management system.
func (tx *Tx) ClosePaperSimulation(spaceID, tradingAccountID string) error {
	account, err := tx.GetTradingAccount(spaceID, tradingAccountID)
	if err != nil {
		return err
	}
	if account.ExecutionMode != "PAPER" {
		return fmt.Errorf("%w: only PAPER accounts can be closed by simulation lifecycle", ErrInvalidRecord)
	}
	orders, err := tx.listOpenOrdersForAccount(spaceID, tradingAccountID)
	if err != nil {
		return err
	}
	for _, current := range orders {
		switch current.State {
		case "OPEN":
			if err := tx.CancelPaperOrder(current, current.Version, "paper simulation closed"); err != nil {
				return err
			}
		default:
			// Closing is terminal for a Paper fixture. Orders that are still in
			// submission/cancel handshakes are rejected and their reservation is
			// released rather than being retried through an exchange adapter.
			result := tx.db.Exec(`
				UPDATE t_trade_orders
				SET c_state='CANCELED', c_first_match_pending=0,
				    c_remaining_reserved_quantity='0', c_reject_reason=?,
				    c_finished_at=CAST(strftime('%s','now') AS INTEGER)*1000,
				    c_version=c_version+1, c_mtime=CURRENT_TIMESTAMP
				WHERE c_space_id=? AND c_order_id=? AND c_version=?`,
				"paper simulation closed", current.SpaceID, current.OrderID, current.Version)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("%w: stale paper order", ErrConflict)
			}
		}
	}
	if err := tx.SetTradingAccountStatus(spaceID, tradingAccountID, "DISABLED"); err != nil {
		return err
	}
	var logicalRows []struct {
		LogicalAccountID string `gorm:"column:c_logical_account_id"`
	}
	if err := tx.db.Raw(`
		SELECT c_logical_account_id
		FROM t_logical_account_members
		WHERE c_space_id = ? AND c_trading_account_id = ? AND c_enabled = 1
	`, spaceID, tradingAccountID).Scan(&logicalRows).Error; err != nil {
		return err
	}
	for _, row := range logicalRows {
		logicalID := row.LogicalAccountID
		if logicalID == "" {
			continue
		}
		result := tx.db.Exec(`
			UPDATE t_logical_accounts SET c_automation_state = 'PAUSED', c_pause_reason = ?, c_mtime = CURRENT_TIMESTAMP
			WHERE c_space_id = ? AND c_logical_account_id = ?
		`, "paper simulation closed", spaceID, logicalID)
		if result.Error != nil {
			return result.Error
		}
	}
	return nil
}

func (tx *Tx) listOpenOrdersForAccount(spaceID, tradingAccountID string) ([]OrderRecord, error) {
	var rows []orderRow
	err := tx.db.Table("t_trade_orders").
		Where("c_space_id = ? AND c_trading_account_id = ? AND c_state NOT IN ?", spaceID, tradingAccountID,
			[]string{"FILLED", "CANCELED", "PARTIALLY_CANCELED", "REJECTED", "EXPIRED"}).
		Order("c_order_id").Find(&rows).Error
	if err != nil {
		return nil, err
	}
	result := make([]OrderRecord, 0, len(rows))
	for _, row := range rows {
		result = append(result, orderRecordFromRow(row))
	}
	return result, nil
}

func (tx *Tx) SetTradingAccountStatus(spaceID, tradingAccountID, status string) error {
	result := tx.db.Exec(`
		UPDATE t_trading_accounts SET c_status = ?, c_ready = 0, c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_trading_account_id = ?
	`, status, spaceID, tradingAccountID)
	return requireUpdated(result.Error, result.RowsAffected, "trading account status")
}

func (tx *Tx) PaperSimulationMemberCount(spaceID, logicalAccountID string) (int64, error) {
	var count int64
	err := tx.db.Table("t_logical_account_members").
		Where("c_space_id = ? AND c_logical_account_id = ? AND c_enabled = 1", spaceID, logicalAccountID).
		Count(&count).Error
	return count, err
}
