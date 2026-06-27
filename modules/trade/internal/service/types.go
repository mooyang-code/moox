// Package service 实现 Trade 模块的领域服务层。
//
// 组织方式（对齐 admin 模块）：按域拆子能力——账户域（account）与交易域（order）。
// 本文件定义跨域共享的领域模型与错误，避免直接依赖尚未生成的 PB；
// 待 proto 生成后，RPC handler 层负责 PB <-> 领域模型的转换。
package service

import "errors"

// 领域层通用错误。
var (
	ErrInvalidParam = errors.New("trade: invalid parameter")
	ErrNotFound     = errors.New("trade: resource not found")
	ErrConflict     = errors.New("trade: resource conflict")
	ErrInsufficient = errors.New("trade: insufficient balance")
)

// ===== 账户域模型 =====

// AccountType 账户类型。
type AccountType string

const (
	AccountSpot   AccountType = "spot"
	AccountMargin AccountType = "margin"
	AccountSwap   AccountType = "swap"
	AccountSim    AccountType = "sim"
)

// AccountStatus 账户状态。
type AccountStatus int

const (
	AccountDisabled AccountStatus = 0
	AccountNormal   AccountStatus = 1
	AccountFrozen   AccountStatus = 2
	AccountReadonly AccountStatus = 3
)

// Account 账户（对应 t_accounts）。
type Account struct {
	AccountID    string
	UserID       string
	AccountName  string
	AccountType  AccountType
	ChannelID    string
	BaseCurrency string
	Status       AccountStatus
	IsDefault    bool
	Remark       string
	CreatedAt    int64
	UpdatedAt    int64
}

// Balance 余额（对应 t_account_balances）。
type Balance struct {
	AccountID string
	Currency  string
	Available string
	Frozen    string
	Total     string
	Version   int64
}

// FundFlow 资金流水（对应 t_account_fund_flows）。
type FundFlow struct {
	FlowID       string
	AccountID    string
	Currency     string
	BizType      string
	Direction    int
	Amount       string
	BalanceAfter string
	RefType      string
	RefID        string
	Remark       string
	CreatedAt    int64
}

// APIKey API 凭证（对应 t_account_api_keys）。敏感字段在 DAO 层加解密。
type APIKey struct {
	APIKeyID    string
	AccountID   string
	Exchange    string
	APIKey      string
	APISecret   string
	Passphrase  string
	Permissions []string
	Status      int
	CreatedAt   int64
}

// ===== 交易域模型 =====

// TradeChannel 交易通道（对应 t_trade_channels）。
type TradeChannel struct {
	ChannelID     string
	ChannelName   string
	Exchange      string
	MarketType    string
	AccountID     string
	APIKeyID      string
	Endpoint      string
	IsSimulated   bool
	Status        int
	RateLimit     int
	LastHeartbeat int64
	CreatedAt     int64
	UpdatedAt     int64
}

// Order 订单（对应 t_orders）。
type Order struct {
	OrderID         string
	ClientOrderID   string
	ExchangeOrderID string
	AccountID       string
	ChannelID       string
	Exchange        string
	Symbol          string
	MarketType      string
	Side            string
	PosSide         string
	OrderType       string
	TimeInForce     string
	Price           string
	Quantity        string
	Amount          string
	FilledQty       string
	FilledAmount    string
	AvgPrice        string
	Fee             string
	FeeCurrency     string
	Status          int
	ReduceOnly      bool
	TriggerPrice    string
	Source          string
	StrategyID      string
	RejectReason    string
	SubmittedAt     int64
	FinishedAt      int64
	CreatedAt       int64
	UpdatedAt       int64
}

// Trade 成交明细（对应 t_trades）。
type Trade struct {
	TradeID         string
	ExchangeTradeID string
	OrderID         string
	AccountID       string
	ChannelID       string
	Exchange        string
	Symbol          string
	Side            string
	Price           string
	Quantity        string
	Amount          string
	Fee             string
	FeeCurrency     string
	Role            string
	TradedAt        int64
}

// Position 持仓（对应 t_positions）。
type Position struct {
	PositionID    string
	AccountID     string
	ChannelID     string
	Exchange      string
	Symbol        string
	PosSide       string
	Quantity      string
	AvgPrice      string
	Leverage      string
	Margin        string
	LiqPrice      string
	UnrealizedPnl string
	RealizedPnl   string
	UpdatedAt     int64
}

// Page 分页入参。
type Page struct {
	PageNo   int
	PageSize int
}

// Normalize 修正非法分页值。
func (p Page) Normalize() Page {
	if p.PageNo <= 0 {
		p.PageNo = 1
	}
	if p.PageSize <= 0 || p.PageSize > 200 {
		p.PageSize = 20
	}
	return p
}

// Offset 计算 SQL 偏移。
func (p Page) Offset() int { return (p.PageNo - 1) * p.PageSize }
