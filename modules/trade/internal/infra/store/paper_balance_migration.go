package store

import (
	"fmt"
	"reflect"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/schema"
	"gorm.io/gorm"
)

// Only the exact previous fills schema may acquire the new history index.
// Unknown columns, constraints, and indexes still fail strict validation.
func migratePaperBalanceHistoryIndex(db *gorm.DB) error {
	const indexName = "idx_order_fills_paper_balance_history"
	var tableExists, indexExists int64
	if err := db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 't_order_fills'").Scan(&tableExists).Error; err != nil {
		return err
	}
	if tableExists == 0 {
		return nil
	}
	if err := db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?", indexName).Scan(&indexExists).Error; err != nil {
		return err
	}
	if indexExists != 0 {
		return nil
	}
	reference, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		return err
	}
	sqlDB, err := reference.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	if err := reference.Exec(schema.AllSQL()).Error; err != nil {
		return err
	}
	var indexSQL string
	if err := reference.Raw("SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?", indexName).Scan(&indexSQL).Error; err != nil {
		return err
	}
	if err := reference.Exec("DROP INDEX " + indexName).Error; err != nil {
		return err
	}
	want, err := inspectTableShape(reference, "t_order_fills")
	if err != nil {
		return err
	}
	got, err := inspectTableShape(db, "t_order_fills")
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(got, want) {
		return nil
	}
	return db.Exec(indexSQL).Error
}

// Backfill runs before Open returns, while no execution worker can write fills.
// The marker and every balance are committed together with the complete history.
func initializePaperBalances(db *gorm.DB) error {
	return db.Transaction(func(db *gorm.DB) error {
		tx := &Tx{db: db}
		var accounts []struct {
			SpaceID   string `gorm:"column:c_space_id"`
			AccountID string `gorm:"column:c_trading_account_id"`
			Initial   string `gorm:"column:c_initial_balance"`
		}
		if err := db.Raw(`SELECT a.c_space_id, a.c_trading_account_id, c.c_initial_balance
   FROM t_trading_accounts a
   LEFT JOIN t_paper_account_configs c ON c.c_space_id = a.c_space_id AND c.c_trading_account_id = a.c_trading_account_id
   LEFT JOIN t_paper_balance_projections p ON p.c_space_id = a.c_space_id AND p.c_trading_account_id = a.c_trading_account_id
   WHERE a.c_execution_mode = 'PAPER' AND p.c_trading_account_id IS NULL
   ORDER BY a.c_space_id, a.c_trading_account_id`).Scan(&accounts).Error; err != nil {
			return err
		}
		for _, account := range accounts {
			if err := tx.initializePaperBalance(account.SpaceID, account.AccountID, account.Initial); err != nil {
				return fmt.Errorf("initialize paper balance: %w", err)
			}
			var lastTime int64
			var lastID string
			first := true
			for {
				var fills []fillRow
				query := db.Table("t_order_fills").Where("c_space_id = ? AND c_trading_account_id = ?", account.SpaceID, account.AccountID)
				if !first {
					query = query.Where("c_traded_at > ? OR (c_traded_at = ? AND c_fill_id > ?)", lastTime, lastTime, lastID)
				}
				if err := query.Order("c_traded_at ASC, c_fill_id ASC").Limit(1000).Find(&fills).Error; err != nil {
					return err
				}
				if len(fills) == 0 {
					break
				}
				for _, fill := range fills {
					for _, value := range []string{fill.Price, fill.Quantity, fill.Fee, fill.RealizedPnL} {
						if _, err := shared.ParseDecimal(value); err != nil {
							return fmt.Errorf("%w: stored paper fill decimal", ErrInvalidRecord)
						}
					}
					record := FillRecord{SpaceID: fill.SpaceID, TradingAccountID: fill.TradingAccountID,
						FillID: fill.FillID, ExchangeTradeID: fill.ExchangeTradeID, OrderID: fill.OrderID,
						MarketType: fill.MarketType, ExchangeSymbol: fill.ExchangeSymbol,
						Price: fill.Price, Quantity: fill.Quantity, Fee: fill.Fee, FeeAsset: fill.FeeAsset,
						SettlementAsset: fill.SettlementAsset, RealizedPnL: fill.RealizedPnL, Side: fill.Side}
					if err := tx.applyPaperFill(record); err != nil {
						return fmt.Errorf("backfill paper fill: %w", err)
					}
				}
				last := fills[len(fills)-1]
				lastTime, lastID, first = last.TradedAt, last.FillID, false
			}
		}
		return nil
	})
}
