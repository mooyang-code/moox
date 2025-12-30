package model

import (
	"time"
)

// ExchangeSymbol 交易所标的表
type ExchangeSymbol struct {
	// ID 主键ID
	ID int `gorm:"primaryKey;column:c_id;autoIncrement" json:"id"`
	// Exchange 交易所名称（binance/okx/huobi等）
	Exchange string `gorm:"column:c_exchange;index:idx_exchange;not null" json:"exchange"`
	// InstType 产品类型（SPOT/SWAP/FUTURES等）
	InstType string `gorm:"column:c_inst_type;index:idx_inst_type;not null" json:"inst_type"`
	// Symbol 标的符号（BTC-USDT）
	Symbol string `gorm:"column:c_symbol;uniqueIndex:idx_unique;not null" json:"symbol"`
	// BaseCurrency 基础货币（BTC）
	BaseCurrency string `gorm:"column:c_base_currency;not null" json:"base_currency"`
	// QuoteCurrency 计价货币（USDT）
	QuoteCurrency string `gorm:"column:c_quote_currency;not null" json:"quote_currency"`
	// Status 状态（active=活跃，inactive=停用，delisted=退市）
	Status string `gorm:"column:c_status;index:idx_status;not null;default:'active'" json:"status"`

	// MinQty 最小交易数量
	MinQty string `gorm:"column:c_min_qty;not null;default:''" json:"min_qty"`
	// MaxQty 最大交易数量
	MaxQty string `gorm:"column:c_max_qty;not null;default:''" json:"max_qty"`
	// TickSize 价格最小变动单位
	TickSize string `gorm:"column:c_tick_size;not null;default:''" json:"tick_size"`
	// LotSize 数量最小变动单位
	LotSize string `gorm:"column:c_lot_size;not null;default:''" json:"lot_size"`

	// Metadata 扩展元数据（JSON格式）
	Metadata string `gorm:"column:c_metadata;type:text;not null;default:'{}'" json:"metadata"`

	// SyncTime 同步时间戳（毫秒）
	SyncTime int64 `gorm:"column:c_sync_time;index:idx_sync_time;not null" json:"sync_time"`

	// Invalid 删除标记
	Invalid int `gorm:"column:c_invalid;index:idx_invalid;not null;default:0" json:"invalid"`
	// CreateTime 创建时间
	CreateTime time.Time `gorm:"column:c_ctime;type:datetime;default:CURRENT_TIMESTAMP" json:"create_time"`
	// ModifyTime 修改时间
	ModifyTime time.Time `gorm:"column:c_mtime;type:datetime;default:CURRENT_TIMESTAMP" json:"modify_time"`
}

// TableName 指定表名
func (e *ExchangeSymbol) TableName() string {
	return "t_exchange_symbols"
}

// 标的状态常量
const (
	SymbolStatusActive   = "active"   // 活跃
	SymbolStatusInactive = "inactive" // 停用
	SymbolStatusDelisted = "delisted" // 退市
)
