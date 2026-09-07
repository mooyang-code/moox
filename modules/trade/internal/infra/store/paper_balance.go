package store

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

type PaperBalanceSnapshot struct {
	Totals           map[string]shared.Decimal
	Reserved         map[string]shared.Decimal
	AppliedFillCount int64
}

func (tx *Tx) initializePaperBalance(spaceID, accountID, initial string) error {
	account, err := tx.GetTradingAccount(spaceID, accountID)
	if err != nil {
		return err
	}
	if account.ExecutionMode != "PAPER" || account.SettlementAsset == "" {
		return fmt.Errorf("%w: paper balance account", ErrInvalidRecord)
	}
	total, err := canonicalDecimal(initial, "paper initial balance", decimalPositive)
	if err != nil {
		return err
	}
	if err := tx.db.Exec(`INSERT INTO t_paper_balance_projections (c_space_id, c_trading_account_id, c_initialized_at) VALUES (?, ?, ?)`, spaceID, accountID, time.Now().UTC().UnixMilli()).Error; err != nil {
		return writeError(err)
	}
	return tx.db.Exec(`INSERT INTO t_paper_asset_balances (c_space_id, c_trading_account_id, c_asset, c_total) VALUES (?, ?, ?, ?)`, spaceID, accountID, account.SettlementAsset, total).Error
}

func (s *Store) GetPaperBalanceSnapshot(ctx context.Context, spaceID, accountID string) (PaperBalanceSnapshot, error) {
	var result PaperBalanceSnapshot
	err := s.Transaction(ctx, func(tx *Tx) error {
		var err error
		result, err = tx.GetPaperBalanceSnapshot(spaceID, accountID)
		return err
	})
	return result, err
}

func (tx *Tx) GetPaperBalanceSnapshot(spaceID, accountID string) (PaperBalanceSnapshot, error) {
	result := PaperBalanceSnapshot{Totals: map[string]shared.Decimal{}, Reserved: map[string]shared.Decimal{}}
	var marker struct {
		Count int64 `gorm:"column:c_applied_fill_count"`
	}
	if err := tx.db.Table("t_paper_balance_projections").Where("c_space_id = ? AND c_trading_account_id = ?", spaceID, accountID).Take(&marker).Error; err != nil {
		return result, err
	}
	result.AppliedFillCount = marker.Count
	var balances []struct {
		Asset string `gorm:"column:c_asset"`
		Total string `gorm:"column:c_total"`
	}
	if err := tx.db.Table("t_paper_asset_balances").Where("c_space_id = ? AND c_trading_account_id = ?", spaceID, accountID).Find(&balances).Error; err != nil {
		return result, err
	}
	for _, balance := range balances {
		value, err := shared.ParseDecimal(balance.Total)
		if err != nil || balance.Asset == "" {
			return result, fmt.Errorf("%w: paper asset balance", ErrInvalidRecord)
		}
		result.Totals[balance.Asset] = value
	}
	var reservations []struct {
		Asset    string `gorm:"column:c_reserved_asset"`
		Quantity string `gorm:"column:c_remaining_reserved_quantity"`
	}
	if err := tx.db.Raw(`SELECT c_reserved_asset, c_remaining_reserved_quantity FROM t_trade_orders
 WHERE c_space_id = ? AND c_trading_account_id = ? AND c_state IN ('PENDING', 'SUBMITTING', 'SUBMIT_UNKNOWN', 'OPEN', 'PARTIALLY_FILLED', 'CANCELING', 'CANCEL_UNKNOWN')`, spaceID, accountID).Scan(&reservations).Error; err != nil {
		return result, err
	}
	for _, reservation := range reservations {
		quantity, err := shared.ParseDecimal(reservation.Quantity)
		if err != nil || quantity.IsNegative() || (!quantity.IsZero() && reservation.Asset == "") {
			return result, fmt.Errorf("%w: paper reservation", ErrInvalidRecord)
		}
		if !quantity.IsZero() {
			result.Reserved[reservation.Asset] = result.Reserved[reservation.Asset].Add(quantity)
		}
	}
	return result, nil
}

func (tx *Tx) applyPaperFill(record FillRecord) error {
	identity, err := tx.accountIdentity(record.SpaceID, record.TradingAccountID)
	if err != nil {
		return err
	}
	if identity.ExecutionMode != "PAPER" {
		return nil
	}
	if err := tx.normalizePaperFillSettlement(&record); err != nil {
		return err
	}
	if err := canonicalizeFill(&record); err != nil {
		return err
	}
	price, err := shared.ParseDecimal(record.Price)
	if err != nil || price.IsNegative() || price.IsZero() {
		return fmt.Errorf("%w: paper fill price", ErrInvalidRecord)
	}
	quantity, err := shared.ParseDecimal(record.Quantity)
	if err != nil || quantity.IsNegative() || quantity.IsZero() {
		return fmt.Errorf("%w: paper fill quantity", ErrInvalidRecord)
	}
	fee, err := shared.ParseDecimal(record.Fee)
	if err != nil {
		return fmt.Errorf("%w: paper fill fee", ErrInvalidRecord)
	}
	pnl, err := shared.ParseDecimal(record.RealizedPnL)
	if err != nil {
		return fmt.Errorf("%w: paper fill realized PnL", ErrInvalidRecord)
	}
	deltas := map[string]shared.Decimal{}
	switch record.MarketType {
	case "SPOT":
		instrument, err := getInstrument(tx.db, identity.Exchange, identity.Environment, identity.MarketType, record.ExchangeSymbol)
		if err != nil {
			return err
		}
		if instrument.BaseAsset == "" || instrument.QuoteAsset == "" || instrument.BaseAsset == instrument.QuoteAsset {
			return fmt.Errorf("%w: paper spot asset identity", ErrInvalidRecord)
		}
		switch record.Side {
		case "BUY":
			deltas[instrument.BaseAsset] = quantity
			deltas[instrument.QuoteAsset] = quantity.Mul(price).Neg()
		case "SELL":
			deltas[instrument.BaseAsset] = quantity.Neg()
			deltas[instrument.QuoteAsset] = quantity.Mul(price)
		default:
			return fmt.Errorf("%w: paper fill side", ErrInvalidRecord)
		}
	case "SWAP":
		if record.SettlementAsset == "" {
			return fmt.Errorf("%w: paper settlement asset", ErrInvalidRecord)
		}
		deltas[record.SettlementAsset] = pnl
	default:
		return fmt.Errorf("%w: paper fill market", ErrInvalidRecord)
	}
	if !fee.IsZero() {
		asset := record.FeeAsset
		if asset == "" {
			return fmt.Errorf("%w: paper fee asset", ErrInvalidRecord)
		}
		deltas[asset] = deltas[asset].Sub(fee)
	}
	for asset, delta := range deltas {
		var row struct {
			Total string `gorm:"column:c_total"`
		}
		result := tx.db.Raw(`SELECT c_total FROM t_paper_asset_balances WHERE c_space_id = ? AND c_trading_account_id = ? AND c_asset = ?`, record.SpaceID, record.TradingAccountID, asset).Scan(&row)
		if result.Error != nil {
			return result.Error
		}
		total := shared.Zero()
		if result.RowsAffected != 0 {
			total, err = shared.ParseDecimal(row.Total)
			if err != nil {
				return fmt.Errorf("%w: stored paper balance", ErrInvalidRecord)
			}
		}
		next := total.Add(delta).String()
		if _, err := shared.ParseDecimal(next); err != nil {
			return fmt.Errorf("%w: paper balance precision", ErrInvalidRecord)
		}
		if err := tx.db.Exec(`INSERT INTO t_paper_asset_balances (c_space_id, c_trading_account_id, c_asset, c_total) VALUES (?, ?, ?, ?)
   ON CONFLICT (c_space_id, c_trading_account_id, c_asset) DO UPDATE SET c_total = excluded.c_total`, record.SpaceID, record.TradingAccountID, asset, next).Error; err != nil {
			return err
		}
	}
	result := tx.db.Exec(`UPDATE t_paper_balance_projections SET c_applied_fill_count = c_applied_fill_count + 1 WHERE c_space_id = ? AND c_trading_account_id = ?`, record.SpaceID, record.TradingAccountID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w: uninitialized paper balance", ErrInvalidRecord)
	}
	return nil
}

func (tx *Tx) normalizePaperFillSettlement(record *FillRecord) error {
	if record.MarketType != "SWAP" {
		return nil
	}
	var settlement string
	if err := tx.db.Raw(`SELECT c_settlement_asset FROM t_trading_accounts WHERE c_space_id = ? AND c_trading_account_id = ?`, record.SpaceID, record.TradingAccountID).Scan(&settlement).Error; err != nil {
		return err
	}
	if settlement == "" {
		return fmt.Errorf("%w: paper account settlement asset", ErrInvalidRecord)
	}
	return validateOrDeriveIdentity(&record.SettlementAsset, settlement, "paper fill settlement asset")
}
