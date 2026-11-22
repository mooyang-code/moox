package store

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"gorm.io/gorm"
)

type PaperAccountConfigRecord struct {
	SpaceID          string
	TradingAccountID string
	InitialBalance   string
	MakerFeeRate     string
	TakerFeeRate     string
	SlippageBPS      string
	// OrderRetentionDays 是已结束订单与成交保留的天数，0 表示不清理；构造记录时要显式给值（默认 5）。
	OrderRetentionDays int
}

type paperAccountConfigRow struct {
	SpaceID            string `gorm:"column:c_space_id"`
	TradingAccountID   string `gorm:"column:c_trading_account_id"`
	InitialBalance     string `gorm:"column:c_initial_balance"`
	MakerFeeRate       string `gorm:"column:c_maker_fee_rate"`
	TakerFeeRate       string `gorm:"column:c_taker_fee_rate"`
	SlippageBPS        string `gorm:"column:c_slippage_bps"`
	OrderRetentionDays int    `gorm:"column:c_order_retention_days"`
}

func (row paperAccountConfigRow) record() PaperAccountConfigRecord {
	return PaperAccountConfigRecord{SpaceID: row.SpaceID, TradingAccountID: row.TradingAccountID,
		InitialBalance: row.InitialBalance, MakerFeeRate: row.MakerFeeRate,
		TakerFeeRate: row.TakerFeeRate, SlippageBPS: row.SlippageBPS,
		OrderRetentionDays: row.OrderRetentionDays}
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
	if !tradingaccount.ValidOrderRetentionDays(record.OrderRetentionDays) {
		return fmt.Errorf("%w: 订单保留天数要在 0～%d 之间", ErrInvalidRecord, tradingaccount.MaxOrderRetentionDays)
	}
	if err := writeError(tx.db.Exec(execInsertPaperAccountConfig, record.SpaceID, record.TradingAccountID, initial, maker, taker, slippage,
		record.OrderRetentionDays).Error); err != nil {
		return err
	}
	return tx.initializePaperBalance(record.SpaceID, record.TradingAccountID, initial)
}

// SetPaperOrderRetentionDays 改模拟账户的订单保留天数（0 表示不清理）。账户没有模拟配置时报 gorm.ErrRecordNotFound。
func (tx *Tx) SetPaperOrderRetentionDays(spaceID, tradingAccountID string, days int) error {
	if blank(spaceID) || blank(tradingAccountID) {
		return fmt.Errorf("%w: incomplete paper account config", ErrInvalidRecord)
	}
	if !tradingaccount.ValidOrderRetentionDays(days) {
		return fmt.Errorf("%w: 订单保留天数要在 0～%d 之间", ErrInvalidRecord, tradingaccount.MaxOrderRetentionDays)
	}
	result := tx.db.Exec(execUpdatePaperOrderRetentionDays, days, spaceID, tradingAccountID)
	if err := writeError(result.Error); err != nil {
		return err
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (s *Store) GetPaperAccountConfig(ctx context.Context, spaceID, tradingAccountID string) (PaperAccountConfigRecord, error) {
	var row paperAccountConfigRow
	if err := s.db.WithContext(ctx).Table("t_paper_account_configs").
		Where("c_space_id = ? AND c_trading_account_id = ?", spaceID, tradingAccountID).Take(&row).Error; err != nil {
		return PaperAccountConfigRecord{}, err
	}
	return row.record(), nil
}

// 以下 SQL 整条写成常量，不在调用处拼接或内联。
const (
	execInsertPaperAccountConfig = `INSERT INTO t_paper_account_configs (
			c_space_id, c_trading_account_id, c_initial_balance,
			c_maker_fee_rate, c_taker_fee_rate, c_slippage_bps, c_order_retention_days
		) VALUES (?, ?, ?, ?, ?, ?, ?)`
	execUpdatePaperOrderRetentionDays = `UPDATE t_paper_account_configs SET c_order_retention_days = ?
		WHERE c_space_id = ? AND c_trading_account_id = ?`
)
