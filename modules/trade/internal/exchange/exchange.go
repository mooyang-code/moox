package exchange

import "context"

// ExchangeAdapter 是交易所适配的统一抽象接口；每个交易所实现一份。
// 所有方法均按 (Market, Symbol) 路由到对应市场的底层 REST 调用。
// 交易所侧的认证使用每次调用传入的 Credential（由 t_account_api_keys 解密得到）。
type ExchangeAdapter interface {
	// ---- 元信息 / 通道 ----

	// Name 返回交易所标识，如 binance / okx。
	Name() string
	// Ping 校验凭证连通性，返回往返耗时（毫秒）。用于 ChannelSvc.TestChannel。
	Ping(ctx context.Context, cred Credential) (latencyMS int64, err error)
	// GetInstruments 拉取交易规则（精度/最小下单量），下单前本地校验用。
	GetInstruments(ctx context.Context, market MarketType) ([]Instrument, error)

	// ---- 账户 / 余额 ----

	GetAccountInfo(ctx context.Context, cred Credential, market MarketType) (*AccountInfo, error)
	GetBalances(ctx context.Context, cred Credential, market MarketType, currencies []string) ([]Balance, error)
	GetTradeFee(ctx context.Context, cred Credential, market MarketType, symbol string) (*FeeRate, error)

	// ---- 资金 ----

	ListFundFlows(ctx context.Context, cred Credential, req *FundFlowQuery) ([]FundFlow, error)
	Transfer(ctx context.Context, cred Credential, req *TransferReq) (*TransferResult, error)

	// ---- 下单 / 撤单 / 改单 / 杠杆 ----

	PlaceOrder(ctx context.Context, cred Credential, req *PlaceOrderReq) (*OrderResult, error)
	CancelOrder(ctx context.Context, cred Credential, req *CancelOrderReq) (*OrderResult, error)
	// CancelAllOrders 撤销某市场下指定 symbol 的全部挂单（symbol 为空表示全部）。
	CancelAllOrders(ctx context.Context, cred Credential, market MarketType, symbol string) (canceled int, err error)
	// AmendOrder 改单。Binance 现货无原生改单，实现内部退化为「撤单 + 重下」（用 ClientOrderID 保幂等）。
	AmendOrder(ctx context.Context, cred Credential, req *AmendOrderReq) (*OrderResult, error)
	SetLeverage(ctx context.Context, cred Credential, market MarketType, symbol, leverage string) error
	// ClosePosition 市价平仓（合约）。
	ClosePosition(ctx context.Context, cred Credential, market MarketType, symbol, posSide string) error

	// ---- 查询 ----

	GetOrder(ctx context.Context, cred Credential, req *GetOrderReq) (*Order, error)
	ListOpenOrders(ctx context.Context, cred Credential, req *ListOrdersReq) ([]Order, error)
	ListOrders(ctx context.Context, cred Credential, req *ListOrdersReq) ([]Order, error)
	ListTrades(ctx context.Context, cred Credential, req *ListTradesReq) ([]Trade, error)
	ListPositions(ctx context.Context, cred Credential, market MarketType, symbol string) ([]Position, error)
}

// PrivateStream 私有 WebSocket 回报（订单/成交/持仓/余额变更），用于实时回填本地表。
// 推荐 ws 为主、REST 查询为兜底。
type PrivateStream interface {
	// Subscribe 订阅账户私有频道；事件经 handler 回调写入
	// t_orders / t_trades / t_positions / t_account_balances。
	Subscribe(ctx context.Context, cred Credential, market MarketType, handler StreamHandler) error
	// Close 关闭连接。
	Close() error
}

// StreamHandler 处理私有频道推送事件。
type StreamHandler interface {
	OnOrderUpdate(evt *OrderEvent)
	OnTrade(evt *TradeEvent)
	OnPositionUpdate(evt *PositionEvent)
	OnBalanceUpdate(evt *BalanceEvent)
	OnError(err error)
}
