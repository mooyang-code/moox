package store

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

type PaperAccountConfigRecord struct {
	SpaceID          string
	TradingAccountID string
	InitialBalance   string
	MakerFeeRate     string
	TakerFeeRate     string
	SlippageBPS      string
}

func (tx *Tx) CreatePaperAccountConfig(record PaperAccountConfigRecord) error {
	if record.SpaceID == "" || record.TradingAccountID == "" {
		return fmt.Errorf("%w: incomplete paper account config", ErrInvalidRecord)
	}
	initial, err := canonicalDecimal(record.InitialBalance, "initial balance", decimalPositive)
	if err != nil {
		return err
	}
	maker, err := canonicalDecimal(record.MakerFeeRate, "maker fee rate", decimalNonNegative)
	if err != nil {
		return err
	}
	taker, err := canonicalDecimal(record.TakerFeeRate, "taker fee rate", decimalNonNegative)
	if err != nil {
		return err
	}
	slippage, err := canonicalDecimal(record.SlippageBPS, "slippage bps", decimalNonNegative)
	if err != nil {
		return err
	}
	slipValue, _ := shared.ParseDecimal(slippage)
	if slipValue.Cmp(shared.MustDecimal("10000")) >= 0 {
		return fmt.Errorf("%w: slippage bps must be below 10000", ErrInvalidRecord)
	}
	return writeError(tx.db.Exec(`
		INSERT INTO t_paper_account_configs (
			c_space_id, c_trading_account_id, c_initial_balance,
			c_maker_fee_rate, c_taker_fee_rate, c_slippage_bps
		) VALUES (?, ?, ?, ?, ?, ?)
	`, record.SpaceID, record.TradingAccountID, initial, maker, taker, slippage).Error)
}

func (s *Store) GetPaperAccountConfig(ctx context.Context, spaceID, tradingAccountID string) (PaperAccountConfigRecord, error) {
	var row struct {
		SpaceID          string `gorm:"column:c_space_id"`
		TradingAccountID string `gorm:"column:c_trading_account_id"`
		InitialBalance   string `gorm:"column:c_initial_balance"`
		MakerFeeRate     string `gorm:"column:c_maker_fee_rate"`
		TakerFeeRate     string `gorm:"column:c_taker_fee_rate"`
		SlippageBPS      string `gorm:"column:c_slippage_bps"`
	}
	if err := s.db.WithContext(ctx).Table("t_paper_account_configs").
		Where("c_space_id = ? AND c_trading_account_id = ?", spaceID, tradingAccountID).Take(&row).Error; err != nil {
		return PaperAccountConfigRecord{}, err
	}
	return PaperAccountConfigRecord{SpaceID: row.SpaceID, TradingAccountID: row.TradingAccountID,
		InitialBalance: row.InitialBalance, MakerFeeRate: row.MakerFeeRate,
		TakerFeeRate: row.TakerFeeRate, SlippageBPS: row.SlippageBPS}, nil
}
