package dao

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/collectmgr/model"

	"gorm.io/gorm"
)

// ExchangeSymbolDAO 交易所标的数据访问对象接口
type ExchangeSymbolDAO interface {
	// ListActiveSymbols 获取活跃标的列表（用于SymbolProvider）
	ListActiveSymbols(ctx context.Context, exchange, instType string) ([]string, error)

	// GetSymbol 获取单个标的
	GetSymbol(ctx context.Context, exchange, instType, symbol string) (*model.ExchangeSymbol, error)

	// BatchUpsert 批量插入或更新标的（用于同步）
	BatchUpsert(ctx context.Context, symbols []*model.ExchangeSymbol) error

	// ListSymbols 查询标的列表
	ListSymbols(ctx context.Context, exchange, instType, status string, limit int) ([]*model.ExchangeSymbol, error)

	// UpdateStatus 更新标的状态
	UpdateStatus(ctx context.Context, exchange, instType, symbol, status string) error

	// MarkInactive 标记不在新列表中的标的为 inactive（用于全量同步）
	MarkInactive(ctx context.Context, exchange, instType string, activeSymbols []string, syncTime int64) error
}

type exchangeSymbolDaoImpl struct {
	db *gorm.DB
}

// NewExchangeSymbolDAO 创建新的交易所标的DAO实例
func NewExchangeSymbolDAO(db *gorm.DB) ExchangeSymbolDAO {
	return &exchangeSymbolDaoImpl{db: db}
}

// ListActiveSymbols 获取活跃标的列表
func (d *exchangeSymbolDaoImpl) ListActiveSymbols(ctx context.Context, exchange, instType string) ([]string, error) {
	var symbols []string
	result := d.db.WithContext(ctx).
		Model(&model.ExchangeSymbol{}).
		Where("c_exchange = ? AND c_inst_type = ? AND c_status = ? AND c_invalid = ?",
			exchange, instType, model.SymbolStatusActive, model.InvalidNo).
		Pluck("c_symbol", &symbols)

	if result.Error != nil {
		return nil, fmt.Errorf("failed to list active symbols: %w", result.Error)
	}
	return symbols, nil
}

// GetSymbol 获取单个标的
func (d *exchangeSymbolDaoImpl) GetSymbol(ctx context.Context, exchange, instType, symbol string) (*model.ExchangeSymbol, error) {
	var s model.ExchangeSymbol
	result := d.db.WithContext(ctx).
		Where("c_exchange = ? AND c_inst_type = ? AND c_symbol = ? AND c_invalid = ?",
			exchange, instType, symbol, model.InvalidNo).
		First(&s)

	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get symbol: %w", result.Error)
	}
	return &s, nil
}

// BatchUpsert 批量插入或更新标的
func (d *exchangeSymbolDaoImpl) BatchUpsert(ctx context.Context, symbols []*model.ExchangeSymbol) error {
	if len(symbols) == 0 {
		return nil
	}

	// 使用事务批量处理
	return d.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, symbol := range symbols {
			// 查询是否已存在
			var existing model.ExchangeSymbol
			result := tx.Where("c_exchange = ? AND c_inst_type = ? AND c_symbol = ? AND c_invalid = ?",
				symbol.Exchange, symbol.InstType, symbol.Symbol, model.InvalidNo).
				First(&existing)

			now := time.Now()
			if errors.Is(result.Error, gorm.ErrRecordNotFound) {
				// 不存在，插入新记录
				symbol.CreateTime = now
				symbol.ModifyTime = now
				symbol.Invalid = model.InvalidNo
				if err := tx.Create(symbol).Error; err != nil {
					return fmt.Errorf("failed to create symbol %s: %w", symbol.Symbol, err)
				}
			} else if result.Error != nil {
				return fmt.Errorf("failed to query symbol %s: %w", symbol.Symbol, result.Error)
			} else {
				// 已存在，更新记录
				updates := map[string]interface{}{
					"c_base_currency":  symbol.BaseCurrency,
					"c_quote_currency": symbol.QuoteCurrency,
					"c_status":         symbol.Status,
					"c_min_qty":        symbol.MinQty,
					"c_max_qty":        symbol.MaxQty,
					"c_tick_size":      symbol.TickSize,
					"c_lot_size":       symbol.LotSize,
					"c_metadata":       symbol.Metadata,
					"c_sync_time":      symbol.SyncTime,
					"c_mtime":          now,
				}
				if err := tx.Model(&existing).Updates(updates).Error; err != nil {
					return fmt.Errorf("failed to update symbol %s: %w", symbol.Symbol, err)
				}
			}
		}
		return nil
	})
}

// ListSymbols 查询标的列表
func (d *exchangeSymbolDaoImpl) ListSymbols(ctx context.Context, exchange, instType, status string, limit int) ([]*model.ExchangeSymbol, error) {
	var symbols []*model.ExchangeSymbol
	query := d.db.WithContext(ctx).Where("c_invalid = ?", model.InvalidNo)

	if exchange != "" {
		query = query.Where("c_exchange = ?", exchange)
	}
	if instType != "" {
		query = query.Where("c_inst_type = ?", instType)
	}
	if status != "" {
		query = query.Where("c_status = ?", status)
	}

	query = query.Order("c_sync_time DESC")
	if limit > 0 {
		query = query.Limit(limit)
	}

	result := query.Find(&symbols)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to list symbols: %w", result.Error)
	}
	return symbols, nil
}

// UpdateStatus 更新标的状态
func (d *exchangeSymbolDaoImpl) UpdateStatus(ctx context.Context, exchange, instType, symbol, status string) error {
	result := d.db.WithContext(ctx).
		Model(&model.ExchangeSymbol{}).
		Where("c_exchange = ? AND c_inst_type = ? AND c_symbol = ? AND c_invalid = ?",
			exchange, instType, symbol, model.InvalidNo).
		Updates(map[string]interface{}{
			"c_status": status,
			"c_mtime":  time.Now(),
		})

	if result.Error != nil {
		return fmt.Errorf("failed to update symbol status: %w", result.Error)
	}

	if result.RowsAffected == 0 {
		return fmt.Errorf("symbol not found")
	}
	return nil
}

// MarkInactive 标记不在新列表中的标的为 inactive
func (d *exchangeSymbolDaoImpl) MarkInactive(ctx context.Context, exchange, instType string, activeSymbols []string, syncTime int64) error {
	// 如果没有活跃标的，不做任何操作
	if len(activeSymbols) == 0 {
		return nil
	}

	result := d.db.WithContext(ctx).
		Model(&model.ExchangeSymbol{}).
		Where("c_exchange = ? AND c_inst_type = ? AND c_symbol NOT IN ? AND c_status = ? AND c_invalid = ?",
			exchange, instType, activeSymbols, model.SymbolStatusActive, model.InvalidNo).
		Updates(map[string]interface{}{
			"c_status":    model.SymbolStatusInactive,
			"c_sync_time": syncTime,
			"c_mtime":     time.Now(),
		})

	if result.Error != nil {
		return fmt.Errorf("failed to mark inactive symbols: %w", result.Error)
	}
	return nil
}
