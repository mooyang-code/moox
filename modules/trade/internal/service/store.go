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
	// AdjustFrozen 调整某币种冻结额（delta>0 冻结：available→frozen；delta<0 解冻反向）。
	// total 不变。乐观锁 c_version 防并发。供下单冻结/撤单解冻/成交结算使用。
	AdjustFrozen(ctx context.Context, spaceID, accountID, currency, delta string) error

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

	// ---- 操作审计 ----

	// AppendOrderOperation 追加一次通道操作审计（下单/撤单/改单/查询等）。
	AppendOrderOperation(ctx context.Context, spaceID string, op *OrderOperation) error
	// UpdateOrderOperation 回填操作结果（状态/响应/耗时/错误）。
	UpdateOrderOperation(ctx context.Context, spaceID string, op *OrderOperation) error

	// ---- 定时同步游标 ----

	GetSyncCursor(ctx context.Context, spaceID, accountID string, syncType SyncType, symbol string) (*SyncCursor, error)
	UpsertSyncCursor(ctx context.Context, spaceID string, cursor *SyncCursor) error
	ListSyncCursors(ctx context.Context, spaceID, accountID string, syncType SyncType) ([]*SyncCursor, error)
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
