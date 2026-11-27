package store

import (
	"fmt"
	"time"

	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
)

// ClosePaperAccount 原子关停一个模拟盘执行账户：撤消全部未结订单、刷新关闭后余额快照、
// 停用执行账户，并暂停所属组合账户同时移除该成员关系。
func (tx *Tx) ClosePaperAccount(spaceID, tradingAccountID string, now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	account, err := tx.GetTradingAccount(spaceID, tradingAccountID)
	if err != nil {
		return err
	}
	if account.ExecutionMode != "PAPER" {
		return fmt.Errorf("%w: only PAPER accounts can be closed by paper lifecycle", ErrInvalidRecord)
	}
	orders, err := tx.listOpenOrdersForAccount(spaceID, tradingAccountID)
	if err != nil {
		return err
	}
	for _, current := range orders {
		switch current.State {
		case "OPEN":
			if err := tx.CancelPaperOrder(current, current.Version, orderdomain.RejectCodePaperClosed, "模拟账户已关闭"); err != nil {
				return err
			}
		default:
			// Closing is terminal for a Paper account. Orders that are still in
			// submission/cancel handshakes are canceled and their reservation is
			// released rather than being retried through an exchange adapter.
			result := tx.db.Exec(execCancelClosedPaperOrder,
				string(orderdomain.RejectCodePaperClosed), "模拟账户已关闭", now.UTC().UnixMilli(),
				current.SpaceID, current.OrderID, current.Version,
			)
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
	if err := tx.db.Raw(sqlPaperAccountLogicalIDs, spaceID, tradingAccountID).Scan(&logicalRows).Error; err != nil {
		return err
	}
	for _, row := range logicalRows {
		logicalID := row.LogicalAccountID
		if logicalID == "" {
			continue
		}
		// 关闭模拟账户是用户的操作，组合按人工暂停记。
		if err := tx.PauseLogicalAccount(spaceID, logicalID, UserPause("模拟账户已关闭，自动执行已暂停")); err != nil {
			return err
		}
		// 关闭后账户不再持仓和下单，移出成员，组合账户里其他成员不受影响。
		if err := tx.db.Exec(execRemovePaperMember, spaceID, logicalID, tradingAccountID).Error; err != nil {
			return err
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
	var usedMargin, unrealizedPnL shared.Decimal
	valuedAt := snapshot.ExchangeUpdatedAt
	if account.HasPositionMarket() {
		usedMargin, unrealizedPnL, valuedAt, err = tx.closedPaperPositionValuation(account)
		if err != nil {
			return err
		}
		snapshot.ExchangeUpdatedAt = valuedAt
	}

	// 关闭时无实时报价：含余额市场的账户保留快照里已有的净值（现货持仓按最后报价的市值不丢）；
	// 只有合约的账户净值由唯一公式算作现金 + 浮盈亏。
	var preserveEquity *shared.Decimal
	if account.HasBalanceMarket() && snapshot.Equity != "" {
		if cached, parseErr := shared.ParseDecimal(snapshot.Equity); parseErr == nil {
			preserveEquity = &cached
		}
	}
	computed := tradingaccount.ComputePaperEquity(tradingaccount.PaperEquityInput{
		SettlementAsset:      account.SettlementAsset,
		HasBalanceMarket:     account.HasBalanceMarket(),
		HasPositionMarket:    account.HasPositionMarket(),
		Totals:               projection.Totals,
		UsedMargin:           usedMargin,
		UnrealizedPnL:        unrealizedPnL,
		PreserveCachedEquity: preserveEquity,
	})

	snapshot.Balances = make([]AssetBalance, 0, len(computed.Balances))
	for _, row := range computed.Balances {
		snapshot.Balances = append(snapshot.Balances, AssetBalance{
			Wallet:    string(row.Wallet),
			Asset:     row.Asset,
			Available: row.Available.String(),
			Locked:    "0",
			Total:     row.Total.String(),
			Borrowed:  "0",
		})
	}
	snapshot.AvailableFunds = computed.AvailableFunds.String()
	if account.HasPositionMarket() {
		snapshot.UsedMargin = computed.UsedMargin.String()
		snapshot.UnrealizedPnL = computed.UnrealizedPnL.String()
		if !account.HasBalanceMarket() {
			snapshot.Equity = computed.Equity.String()
		}
	}
	encoded, err := encodeSnapshot(snapshot)
	if err != nil {
		return err
	}
	result := tx.db.Exec(execUpdateClosedPaperSnapshot, encoded, account.SpaceID, account.TradingAccountID)
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
		Where("c_space_id = ? AND c_trading_account_id = ? AND c_state IN ?", spaceID, tradingAccountID, activeOrderStates).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return sortedOrderRecords(rows), nil
}

func (tx *Tx) SetTradingAccountStatus(spaceID, tradingAccountID, status string) error {
	result := tx.db.Exec(execSetTradingAccountStatus, status, spaceID, tradingAccountID)
	return requireUpdated(result.Error, result.RowsAffected, "trading account status")
}

// 以下 SQL 整条写成常量，不在调用处拼接或内联。
const (
	execCancelClosedPaperOrder = `UPDATE t_trade_orders
		SET c_state = 'CANCELED', c_first_match_pending = 0,
			c_remaining_reserved_quantity = '0', c_reject_code = ?, c_reject_reason = ?,
			c_finished_at = ?,
			c_version = c_version + 1, c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_order_id = ? AND c_version = ?`
	execUpdateClosedPaperSnapshot = `UPDATE t_trading_accounts SET c_snapshot_json = ?, c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_trading_account_id = ?`
	execSetTradingAccountStatus = `UPDATE t_trading_accounts SET c_status = ?, c_ready = 0, c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_trading_account_id = ?`
	sqlPaperAccountLogicalIDs = `SELECT c_logical_account_id FROM t_logical_account_members
		WHERE c_space_id = ? AND c_trading_account_id = ?`
	execRemovePaperMember = `DELETE FROM t_logical_account_members
		WHERE c_space_id = ? AND c_logical_account_id = ? AND c_trading_account_id = ?`
)
