package service

import "context"

// Store 抽象 Trade 模块的持久化层（DAO）。
// 账户域与交易域共用同一库（同一 SQLite 文件），便于在同一本地事务内完成
// 「下单 → 冻结 → 成交 → 结算 → 刷新余额」。具体实现位于 internal/service/dao。
type Store interface {
	// ---- 账户域 ----

	CreateAccount(ctx context.Context, spaceID string, a *Account) error
	UpdateAccount(ctx context.Context, spaceID string, a *Account) error
	DeleteAccount(ctx context.Context, spaceID, accountID string) error
	GetAccount(ctx context.Context, spaceID, accountID string) (*Account, error)
	ListAccounts(ctx context.Context, spaceID string, f AccountFilter, page Page) ([]*Account, int, error)

	GetBalances(ctx context.Context, spaceID, accountID string, currencies []string) ([]*Balance, error)
	UpsertBalances(ctx context.Context, spaceID string, balances []*Balance) error

	ListFundFlows(ctx context.Context, spaceID string, f FundFlowFilter, page Page) ([]*FundFlow, int, error)
	// AppendFundFlows 追加流水（成对划转/成交结算），与余额更新应在同一事务内。
	AppendFundFlows(ctx context.Context, spaceID string, flows []*FundFlow) error

	CreateAPIKey(ctx context.Context, spaceID string, k *APIKey) error
	DeleteAPIKey(ctx context.Context, spaceID, apiKeyID string) error
	ListAPIKeys(ctx context.Context, spaceID, accountID string) ([]*APIKey, error)
	GetAPIKey(ctx context.Context, spaceID, apiKeyID string) (*APIKey, error)

	// ---- 交易域 ----

	CreateChannel(ctx context.Context, spaceID string, c *TradeChannel) error
	UpdateChannel(ctx context.Context, spaceID string, c *TradeChannel) error
	DeleteChannel(ctx context.Context, spaceID, channelID string) error
	GetChannel(ctx context.Context, spaceID, channelID string) (*TradeChannel, error)
	ListChannels(ctx context.Context, spaceID string, f ChannelFilter, page Page) ([]*TradeChannel, int, error)

	SaveOrder(ctx context.Context, spaceID string, o *Order) error
	UpdateOrder(ctx context.Context, spaceID string, o *Order) error
	GetOrder(ctx context.Context, spaceID, orderID, clientOrderID string) (*Order, error)
	ListOrders(ctx context.Context, spaceID string, f OrderFilter, page Page) ([]*Order, int, error)

	AppendTrades(ctx context.Context, spaceID string, trades []*Trade) error
	ListTrades(ctx context.Context, spaceID string, f TradeFilter, page Page) ([]*Trade, int, error)

	UpsertPositions(ctx context.Context, spaceID string, positions []*Position) error
	ListPositions(ctx context.Context, spaceID, accountID, symbol string) ([]*Position, error)
}

// AccountFilter 账户查询过滤。
type AccountFilter struct {
	UserID      string
	AccountType AccountType
	Keyword     string
}

// FundFlowFilter 资金流水查询过滤。
type FundFlowFilter struct {
	AccountID string
	Currency  string
	BizType   string
	StartTime int64
	EndTime   int64
}

// ChannelFilter 交易通道查询过滤。
type ChannelFilter struct {
	AccountID string
	Exchange  string
}

// OrderFilter 订单查询过滤。
type OrderFilter struct {
	AccountID string
	ChannelID string
	Symbol    string
	Status    int
	OnlyOpen  bool
	StartTime int64
	EndTime   int64
}

// TradeFilter 成交查询过滤。
type TradeFilter struct {
	AccountID string
	OrderID   string
	Symbol    string
	StartTime int64
	EndTime   int64
}
