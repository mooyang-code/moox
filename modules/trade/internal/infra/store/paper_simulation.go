package store

import (
	"fmt"
	"sort"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
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
	if err := tx.refreshClosedPaperBalances(account); err != nil {
		return err
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

func (tx *Tx) refreshClosedPaperBalances(account TradingAccountRecord) error {
	projection, err := tx.GetPaperBalanceSnapshot(account.SpaceID, account.TradingAccountID)
	if err != nil {
		return err
	}
	snapshot := account.Snapshot
	assets := make([]string, 0, len(projection.Totals))
	for asset := range projection.Totals {
		assets = append(assets, asset)
	}
	sort.Strings(assets)
	snapshot.Balances = make([]AssetBalance, 0, len(assets))
	for _, asset := range assets {
		total := projection.Totals[asset].String()
		snapshot.Balances = append(snapshot.Balances, AssetBalance{Asset: asset, Total: total, Available: total, Locked: "0"})
	}
	// Closing must work without quotes. Cash is authoritative, but mark-based
	// valuation remains cached and none of its freshness timestamps advance.
	cash := projection.Totals[account.SettlementAsset]
	snapshot.AvailableFunds = cash.String()
	if account.MarketType == "SWAP" {
		margin, unrealized, valuedAt, err := tx.closedPaperPositionValuation(account)
		if err != nil {
			return err
		}
		equity := cash.Add(unrealized)
		snapshot.UsedMargin = margin.String()
		snapshot.UnrealizedPnL = unrealized.String()
		snapshot.ExchangeUpdatedAt = valuedAt
		snapshot.Equity = equity.String()
		snapshot.AvailableFunds = equity.Sub(margin).String()
	}
	encoded, err := encodeSnapshot(snapshot)
	if err != nil {
		return err
	}
	result := tx.db.Exec(`UPDATE t_trading_accounts SET c_snapshot_json = ?, c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_trading_account_id = ?`, encoded, account.SpaceID, account.TradingAccountID)
	return requireUpdated(result.Error, result.RowsAffected, "closed paper balance snapshot")
}

func (tx *Tx) closedPaperPositionValuation(account TradingAccountRecord) (shared.Decimal, shared.Decimal, int64, error) {
	var positions []struct {
		Quantity   string `gorm:"column:c_signed_quantity"`
		Entry      string `gorm:"column:c_entry_price"`
		Mark       string `gorm:"column:c_mark_price"`
		Leverage   string `gorm:"column:c_leverage"`
		Margin     string `gorm:"column:c_used_margin"`
		Unrealized string `gorm:"column:c_unrealized_pnl"`
		ValuedAt   int64  `gorm:"column:c_exchange_updated_at"`
	}
	margin, unrealized := shared.Zero(), shared.Zero()
	valuedAt := account.Snapshot.ExchangeUpdatedAt
	if valuedAt < 0 {
		valuedAt = 0
	}
	if err := tx.db.Table("t_trading_positions").Where("c_space_id = ? AND c_trading_account_id = ?", account.SpaceID, account.TradingAccountID).Find(&positions).Error; err != nil {
		return margin, unrealized, valuedAt, err
	}
	for _, position := range positions {
		quantity, err := shared.ParseDecimal(position.Quantity)
		if err != nil {
			return margin, unrealized, valuedAt, fmt.Errorf("%w: paper position quantity", ErrInvalidRecord)
		}
		if quantity.IsZero() {
			continue
		}
		if position.ValuedAt <= 0 {
			valuedAt = 0
		} else if valuedAt > position.ValuedAt {
			valuedAt = position.ValuedAt
		}
		for _, raw := range []string{position.Entry, position.Mark, position.Leverage} {
			value, err := shared.ParseDecimal(raw)
			if err != nil || value.Cmp(shared.Zero()) <= 0 {
				return margin, unrealized, valuedAt, fmt.Errorf("%w: paper position valuation inputs", ErrInvalidRecord)
			}
		}
		used, err := shared.ParseDecimal(position.Margin)
		if err != nil || used.IsNegative() {
			return margin, unrealized, valuedAt, fmt.Errorf("%w: paper position used margin", ErrInvalidRecord)
		}
		pnl, err := shared.ParseDecimal(position.Unrealized)
		if err != nil {
			return margin, unrealized, valuedAt, fmt.Errorf("%w: paper position unrealized PnL", ErrInvalidRecord)
		}
		// The fill reducer updates these derived fields atomically with the
		// position. Account snapshot refresh may have failed after that commit.
		margin = margin.Add(used)
		unrealized = unrealized.Add(pnl)
	}
	return margin, unrealized, valuedAt, nil
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
