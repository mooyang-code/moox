// Package service 实现 Trade 模块的领域服务层。
//
// 组织方式（对齐 admin 模块）：按域拆子能力——账户域（account）与交易域（order）。
// 领域模型同时作为 gorm 持久化模型（与 admin 的 model 包一致），带 gorm 列标签；
// 列名 c_xxx，软删除 c_is_deleted（bool），时间 c_ctime/c_mtime 为 time.Time，
// 由 schema 中的 mtime 触发器自动刷新 c_mtime。
// RPC handler 层负责 PB <-> 领域模型（含 int64 epoch <-> time.Time）转换。
package service

import (
	"errors"
	"time"
)

// 领域层通用错误。
var (
	ErrInvalidParam = errors.New("trade: invalid parameter")
	ErrNotFound     = errors.New("trade: resource not found")
	ErrConflict     = errors.New("trade: resource conflict")
	ErrInsufficient = errors.New("trade: insufficient balance")
)

// 软删除标记常量（对应列 c_is_deleted）。
const (
	IsDeletedTrue  = true
	IsDeletedFalse = false
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

// AccountTableName 表名常量。
const AccountTableName = "t_accounts"

// Account 账户（对应 t_accounts）。
type Account struct {
	ID           int64         `gorm:"primaryKey;column:c_id;autoIncrement" json:"-"`
	SpaceID      string        `gorm:"column:c_space_id;not null;default:''" json:"-"`
	AccountID    string        `gorm:"column:c_account_id;not null" json:"account_id"`
	UserID       string        `gorm:"column:c_user_id;not null" json:"user_id"`
	AccountName  string        `gorm:"column:c_account_name;not null;default:''" json:"account_name"`
	AccountType  AccountType   `gorm:"column:c_account_type;not null;default:'spot'" json:"account_type"`
	ChannelID    string        `gorm:"column:c_channel_id;not null;default:''" json:"channel_id"`
	BaseCurrency string        `gorm:"column:c_base_currency;not null;default:'USDT'" json:"base_currency"`
	Status       AccountStatus `gorm:"column:c_status;not null;default:1" json:"status"`
	IsDefault    bool          `gorm:"column:c_is_default;not null;default:false" json:"is_default"`
	Remark       string        `gorm:"column:c_remark;not null;default:''" json:"remark"`
	Attributes   string        `gorm:"column:c_attributes;not null;default:'{}'" json:"-"`
	IsDeleted    bool          `gorm:"column:c_is_deleted;not null;default:false" json:"-"`
	CreatedAt    time.Time     `gorm:"column:c_ctime;type:datetime;default:CURRENT_TIMESTAMP" json:"created_at"`
	UpdatedAt    time.Time     `gorm:"column:c_mtime;type:datetime;default:CURRENT_TIMESTAMP" json:"updated_at"`
}

// AccountBalanceTableName 表名常量。
const AccountBalanceTableName = "t_account_balances"

// Balance 余额（对应 t_account_balances）。
type Balance struct {
	ID        int64     `gorm:"primaryKey;column:c_id;autoIncrement" json:"-"`
	SpaceID   string    `gorm:"column:c_space_id;not null;default:''" json:"-"`
	AccountID string    `gorm:"column:c_account_id;not null" json:"account_id"`
	Currency  string    `gorm:"column:c_currency;not null" json:"currency"`
	Available string    `gorm:"column:c_available;not null;default:'0'" json:"available"`
	Frozen    string    `gorm:"column:c_frozen;not null;default:'0'" json:"frozen"`
	Total     string    `gorm:"column:c_total;not null;default:'0'" json:"total"`
	Version   int64     `gorm:"column:c_version;not null;default:0" json:"-"`
	IsDeleted bool      `gorm:"column:c_is_deleted;not null;default:false" json:"-"`
	CreatedAt time.Time `gorm:"column:c_ctime;type:datetime;default:CURRENT_TIMESTAMP" json:"-"`
	UpdatedAt time.Time `gorm:"column:c_mtime;type:datetime;default:CURRENT_TIMESTAMP" json:"-"`
}

// AccountFundFlowTableName 表名常量。
const AccountFundFlowTableName = "t_account_fund_flows"

// FundFlow 资金流水（对应 t_account_fund_flows，只追加不可变）。
type FundFlow struct {
	ID           int64     `gorm:"primaryKey;column:c_id;autoIncrement" json:"-"`
	SpaceID      string    `gorm:"column:c_space_id;not null;default:''" json:"-"`
	FlowID       string    `gorm:"column:c_flow_id;not null" json:"flow_id"`
	AccountID    string    `gorm:"column:c_account_id;not null" json:"account_id"`
	Currency     string    `gorm:"column:c_currency;not null" json:"currency"`
	BizType      string    `gorm:"column:c_biz_type;not null" json:"biz_type"`
	Direction    int       `gorm:"column:c_direction;not null" json:"direction"`
	Amount       string    `gorm:"column:c_amount;not null;default:'0'" json:"amount"`
	BalanceAfter string    `gorm:"column:c_balance_after;not null;default:'0'" json:"balance_after"`
	RefType      string    `gorm:"column:c_ref_type;not null;default:''" json:"ref_type"`
	RefID        string    `gorm:"column:c_ref_id;not null;default:''" json:"ref_id"`
	Remark       string    `gorm:"column:c_remark;not null;default:''" json:"remark"`
	CreatedAt    time.Time `gorm:"column:c_ctime;type:datetime;default:CURRENT_TIMESTAMP" json:"created_at"`
}

// AccountAPIKeyTableName 表名常量。
const AccountAPIKeyTableName = "t_account_api_keys"

// APIKey API 凭证（对应 t_account_api_keys，敏感字段由 DAO 加解密）。
type APIKey struct {
	ID             int64     `gorm:"primaryKey;column:c_id;autoIncrement" json:"-"`
	SpaceID        string    `gorm:"column:c_space_id;not null;default:''" json:"-"`
	APIKeyID       string    `gorm:"column:c_api_key_id;not null" json:"api_key_id"`
	AccountID      string    `gorm:"column:c_account_id;not null" json:"account_id"`
	Exchange       string    `gorm:"column:c_exchange;not null" json:"exchange"`
	APIKey         string    `gorm:"column:c_api_key;not null" json:"api_key"`            // 落库为密文，列表脱敏
	APISecret      string    `gorm:"column:c_api_secret;not null" json:"-"`               // 落库为密文，不出参
	Passphrase     string    `gorm:"column:c_passphrase;not null;default:''" json:"-"`    // 落库为密文，不出参
	Permissions    string    `gorm:"column:c_permissions;not null;default:'[]'" json:"-"` // JSON 数组字符串（持久化）
	PermissionsRaw []string  `gorm:"-" json:"permissions"`                                // 解析后的权限切片（供 RPC 出参）
	Status         int       `gorm:"column:c_status;not null;default:1" json:"status"`
	IsDeleted      bool      `gorm:"column:c_is_deleted;not null;default:false" json:"-"`
	CreatedAt      time.Time `gorm:"column:c_ctime;type:datetime;default:CURRENT_TIMESTAMP" json:"created_at"`
	UpdatedAt      time.Time `gorm:"column:c_mtime;type:datetime;default:CURRENT_TIMESTAMP" json:"-"`
}

// ===== 交易域模型 =====

// TradeChannelTableName 表名常量。
const TradeChannelTableName = "t_trade_channels"

// TradeChannel 交易通道（对应 t_trade_channels）。
type TradeChannel struct {
	ID            int64     `gorm:"primaryKey;column:c_id;autoIncrement" json:"-"`
	SpaceID       string    `gorm:"column:c_space_id;not null;default:''" json:"-"`
	ChannelID     string    `gorm:"column:c_channel_id;not null" json:"channel_id"`
	ChannelName   string    `gorm:"column:c_channel_name;not null" json:"channel_name"`
	Exchange      string    `gorm:"column:c_exchange;not null" json:"exchange"`
	MarketType    string    `gorm:"column:c_market_type;not null;default:'spot'" json:"market_type"`
	AccountID     string    `gorm:"column:c_account_id;not null;default:''" json:"account_id"`
	APIKeyID      string    `gorm:"column:c_api_key_id;not null;default:''" json:"api_key_id"`
	Endpoint      string    `gorm:"column:c_endpoint;not null;default:''" json:"endpoint"`
	IsSimulated   bool      `gorm:"column:c_is_simulated;not null;default:false" json:"is_simulated"`
	Status        int       `gorm:"column:c_status;not null;default:1" json:"status"`
	RateLimit     int       `gorm:"column:c_rate_limit;not null;default:0" json:"rate_limit"`
	LastHeartbeat time.Time `gorm:"column:c_last_heartbeat;type:datetime" json:"last_heartbeat"`
	Config        string    `gorm:"column:c_config;not null;default:'{}'" json:"-"`
	IsDeleted     bool      `gorm:"column:c_is_deleted;not null;default:false" json:"-"`
	CreatedAt     time.Time `gorm:"column:c_ctime;type:datetime;default:CURRENT_TIMESTAMP" json:"created_at"`
	UpdatedAt     time.Time `gorm:"column:c_mtime;type:datetime;default:CURRENT_TIMESTAMP" json:"updated_at"`
}

// OrderOperationTableName 表名常量。
const OrderOperationTableName = "t_order_operations"

// OrderOperation 账户交易操作审计（对应 t_order_operations）。
type OrderOperation struct {
	ID           int64     `gorm:"primaryKey;column:c_id;autoIncrement" json:"-"`
	SpaceID      string    `gorm:"column:c_space_id;not null;default:''" json:"-"`
	OpID         string    `gorm:"column:c_op_id;not null" json:"op_id"`
	AccountID    string    `gorm:"column:c_account_id;not null" json:"account_id"`
	ChannelID    string    `gorm:"column:c_channel_id;not null;default:''" json:"channel_id"`
	OrderID      string    `gorm:"column:c_order_id;not null;default:''" json:"order_id"`
	OpType       string    `gorm:"column:c_op_type;not null" json:"op_type"`
	Request      string    `gorm:"column:c_request;not null;default:'{}'" json:"request"`
	Response     string    `gorm:"column:c_response;not null;default:'{}'" json:"response"`
	OpStatus     int       `gorm:"column:c_op_status;not null;default:0" json:"op_status"`
	ErrorCode    string    `gorm:"column:c_error_code;not null;default:''" json:"error_code"`
	ErrorMessage string    `gorm:"column:c_error_message;not null;default:''" json:"error_message"`
	LatencyMS    int64     `gorm:"column:c_latency_ms;not null;default:0" json:"latency_ms"`
	Operator     string    `gorm:"column:c_operator;not null;default:''" json:"operator"`
	ClientIP     string    `gorm:"column:c_client_ip;not null;default:''" json:"client_ip"`
	CreatedAt    time.Time `gorm:"column:c_ctime;type:datetime;default:CURRENT_TIMESTAMP" json:"created_at"`
	UpdatedAt    time.Time `gorm:"column:c_mtime;type:datetime;default:CURRENT_TIMESTAMP" json:"-"`
}

// SyncType 定时同步游标类型。
type SyncType string

const (
	SyncTypeBalances  SyncType = "balances"
	SyncTypePositions SyncType = "positions"
	SyncTypeOrders    SyncType = "orders"
	SyncTypeTrades    SyncType = "trades"
)

// SyncCursorTableName 表名常量。
const SyncCursorTableName = "t_trade_sync_cursors"

// SyncCursor 记录定时同步增量游标。
type SyncCursor struct {
	ID            int64     `gorm:"primaryKey;column:c_id;autoIncrement" json:"-"`
	SpaceID       string    `gorm:"column:c_space_id;not null;default:''" json:"space_id"`
	AccountID     string    `gorm:"column:c_account_id;not null" json:"account_id"`
	ChannelID     string    `gorm:"column:c_channel_id;not null;default:''" json:"channel_id"`
	Exchange      string    `gorm:"column:c_exchange;not null;default:''" json:"exchange"`
	MarketType    string    `gorm:"column:c_market_type;not null;default:''" json:"market_type"`
	SyncType      SyncType  `gorm:"column:c_sync_type;not null" json:"sync_type"`
	Symbol        string    `gorm:"column:c_symbol;not null;default:''" json:"symbol"`
	CursorStartMS int64     `gorm:"column:c_cursor_start_ms;not null;default:0" json:"cursor_start_ms"`
	CursorEndMS   int64     `gorm:"column:c_cursor_end_ms;not null;default:0" json:"cursor_end_ms"`
	LastSuccessAt time.Time `gorm:"column:c_last_success_at;type:datetime" json:"last_success_at"`
	LastError     string    `gorm:"column:c_last_error;not null;default:''" json:"last_error"`
	IsEnabled     bool      `gorm:"column:c_is_enabled;not null;default:true" json:"is_enabled"`
	CreatedAt     time.Time `gorm:"column:c_ctime;type:datetime;default:CURRENT_TIMESTAMP" json:"created_at"`
	UpdatedAt     time.Time `gorm:"column:c_mtime;type:datetime;default:CURRENT_TIMESTAMP" json:"updated_at"`
}

// SyncOptions 控制定时同步的一次执行范围。
type SyncOptions struct {
	SpaceID          string
	AccountID        string
	Sections         map[SyncType]bool
	WindowHours      int
	PageSize         int
	MaxSymbolsPerRun int
	Now              time.Time
}

// SyncReport 汇总一次定时同步执行结果。
type SyncReport struct {
	SpaceID         string
	AccountsScanned int
	BalancesSynced  int
	PositionsSynced int
	OrdersSynced    int
	TradesSynced    int
	SkippedAccounts int
	Errors          []string
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
	if p.PageSize <= 0 || p.PageSize > 1000 {
		p.PageSize = 20
	}
	return p
}

// Offset 计算 SQL 偏移。
func (p Page) Offset() int { return (p.PageNo - 1) * p.PageSize }

// ===== gorm 表名映射（统一 t_ 前缀） =====

func (Account) TableName() string        { return AccountTableName }
func (Balance) TableName() string        { return AccountBalanceTableName }
func (FundFlow) TableName() string       { return AccountFundFlowTableName }
func (APIKey) TableName() string         { return AccountAPIKeyTableName }
func (TradeChannel) TableName() string   { return TradeChannelTableName }
func (OrderOperation) TableName() string { return OrderOperationTableName }
func (SyncCursor) TableName() string     { return SyncCursorTableName }
