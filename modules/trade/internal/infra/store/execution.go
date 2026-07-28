package store

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

type LedgerTransactionType string

const (
	LedgerReservation        LedgerTransactionType = "RESERVATION"
	LedgerReservationRelease LedgerTransactionType = "RESERVATION_RELEASE"
	LedgerFillSettlement     LedgerTransactionType = "FILL_SETTLEMENT"
	LedgerFee                LedgerTransactionType = "FEE"
	LedgerSyncAdjustment     LedgerTransactionType = "SYNC_ADJUSTMENT"
)

var ErrUnbalancedLedger = errors.New("trade store: unbalanced ledger transaction")

type LedgerEntryRecord struct {
	Asset  string
	Bucket string
	Amount shared.Decimal
}

type LedgerTransactionRecord struct {
	SpaceID           string
	TransactionID     string
	ExchangeAccountID string
	TransactionType   LedgerTransactionType
	SourceType        string
	SourceID          string
	Entries           []LedgerEntryRecord
}

type BalanceProjectionRecord struct {
	SpaceID           string
	ExchangeAccountID string
	Asset             string
	Bucket            string
	Amount            shared.Decimal
	Version           uint64
}

func (tx *Tx) PostLedger(record LedgerTransactionRecord) error {
	if err := validateLedger(record); err != nil {
		return err
	}
	if err := tx.db.Exec(`
		INSERT INTO t_ledger_transactions (
			c_space_id, c_transaction_id, c_exchange_account_id,
			c_transaction_type, c_source_type, c_source_id
		) VALUES (?, ?, ?, ?, ?, ?)
	`,
		record.SpaceID, record.TransactionID, record.ExchangeAccountID,
		record.TransactionType, record.SourceType, record.SourceID,
	).Error; err != nil {
		return writeError(err)
	}
	for entryNo, entry := range record.Entries {
		if err := tx.db.Exec(`
			INSERT INTO t_ledger_entries (
				c_space_id, c_transaction_id, c_entry_no, c_asset, c_bucket, c_amount
			) VALUES (?, ?, ?, ?, ?, ?)
		`,
			record.SpaceID, record.TransactionID, entryNo+1,
			entry.Asset, entry.Bucket, entry.Amount.String(),
		).Error; err != nil {
			return writeError(err)
		}
		if err := tx.addProjection(
			record.SpaceID,
			record.ExchangeAccountID,
			entry.Asset,
			entry.Bucket,
			entry.Amount,
		); err != nil {
			return err
		}
	}
	return nil
}

func (tx *Tx) addProjection(
	spaceID string,
	exchangeAccountID string,
	asset string,
	bucket string,
	delta shared.Decimal,
) error {
	var current struct {
		Amount  string `gorm:"column:c_amount"`
		Version uint64 `gorm:"column:c_version"`
	}
	result := tx.db.Raw(`
		SELECT c_amount, c_version
		FROM t_trade_balance_projections
		WHERE c_space_id = ? AND c_exchange_account_id = ? AND c_asset = ? AND c_bucket = ?
	`, spaceID, exchangeAccountID, asset, bucket).Scan(&current)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return tx.db.Exec(`
			INSERT INTO t_trade_balance_projections (
				c_space_id, c_exchange_account_id, c_asset, c_bucket, c_amount, c_version
			) VALUES (?, ?, ?, ?, ?, 1)
		`, spaceID, exchangeAccountID, asset, bucket, delta.String()).Error
	}
	amount, err := shared.ParseDecimal(current.Amount)
	if err != nil {
		return fmt.Errorf("%w: stored projection amount", ErrInvalidRecord)
	}
	return tx.db.Exec(`
		UPDATE t_trade_balance_projections
		SET c_amount = ?, c_version = c_version + 1, c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_exchange_account_id = ? AND c_asset = ? AND c_bucket = ?
	`, amount.Add(delta).String(), spaceID, exchangeAccountID, asset, bucket).Error
}

func (s *Store) ListBalanceProjections(
	ctx context.Context,
	spaceID string,
	exchangeAccountID string,
) ([]BalanceProjectionRecord, error) {
	var rows []struct {
		SpaceID           string `gorm:"column:c_space_id"`
		ExchangeAccountID string `gorm:"column:c_exchange_account_id"`
		Asset             string `gorm:"column:c_asset"`
		Bucket            string `gorm:"column:c_bucket"`
		Amount            string `gorm:"column:c_amount"`
		Version           uint64 `gorm:"column:c_version"`
	}
	if err := s.db.WithContext(ctx).Raw(`
		SELECT c_space_id, c_exchange_account_id, c_asset, c_bucket, c_amount, c_version
		FROM t_trade_balance_projections
		WHERE c_space_id = ? AND c_exchange_account_id = ?
		ORDER BY c_asset, c_bucket
	`, spaceID, exchangeAccountID).Scan(&rows).Error; err != nil {
		return nil, err
	}
	records := make([]BalanceProjectionRecord, 0, len(rows))
	for _, row := range rows {
		amount, err := shared.ParseDecimal(row.Amount)
		if err != nil {
			return nil, fmt.Errorf("%w: stored projection amount", ErrInvalidRecord)
		}
		records = append(records, BalanceProjectionRecord{
			SpaceID: row.SpaceID, ExchangeAccountID: row.ExchangeAccountID,
			Asset: row.Asset, Bucket: row.Bucket, Amount: amount, Version: row.Version,
		})
	}
	return records, nil
}

func validateLedger(record LedgerTransactionRecord) error {
	if record.SpaceID == "" || record.TransactionID == "" ||
		record.ExchangeAccountID == "" || record.SourceType == "" ||
		record.SourceID == "" || len(record.Entries) < 2 ||
		!validLedgerTransactionType(record.TransactionType) {
		return fmt.Errorf("%w: incomplete ledger transaction", ErrInvalidRecord)
	}
	totals := make(map[string]shared.Decimal)
	for _, entry := range record.Entries {
		if entry.Asset == "" || entry.Bucket == "" {
			return fmt.Errorf("%w: incomplete ledger entry", ErrInvalidRecord)
		}
		totals[entry.Asset] = totals[entry.Asset].Add(entry.Amount)
	}
	assets := make([]string, 0, len(totals))
	for asset := range totals {
		assets = append(assets, asset)
	}
	sort.Strings(assets)
	for _, asset := range assets {
		if !totals[asset].IsZero() {
			return fmt.Errorf("%w: asset %s", ErrUnbalancedLedger, asset)
		}
	}
	return nil
}

func validLedgerTransactionType(value LedgerTransactionType) bool {
	switch value {
	case LedgerReservation,
		LedgerReservationRelease,
		LedgerFillSettlement,
		LedgerFee,
		LedgerSyncAdjustment:
		return true
	default:
		return false
	}
}
