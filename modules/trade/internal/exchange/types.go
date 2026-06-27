// Package exchange 定义 trade 模块对接各交易所的统一适配抽象与领域模型。
//
// 设计目标：对上层 RPC（TradeOpSvc/OrderSvc/...）暴露交易所中立的接口，
// 对下按「交易所 + 市场类型」路由到具体交易所的 REST/WS 实现。
// 金额/数量统一用 string 承载 decimal，避免浮点精度丢失（与 proto/schema 一致）。
package exchange

// MarketType 市场类型，用于在同一交易所内路由不同 base path
// （Binance 现货 /api、U本位 /fapi、币本位 /dapi；OKX 用 instType 区分）。
type MarketType string

const (
	MarketSpot    MarketType = "spot"    // 现货
	MarketMargin  MarketType = "margin"  // 杠杆
	MarketSwap    MarketType = "swap"    // U本位永续
	MarketFutures MarketType = "futures" // 交割/币本位
)

// OrderSide 买卖方向。
type OrderSide string

const (
	SideBuy  OrderSide = "buy"
	SideSell OrderSide = "sell"
)

// OrderType 订单类型。
type OrderType string

const (
	TypeLimit     OrderType = "limit"
	TypeMarket    OrderType = "market"
	TypeStop      OrderType = "stop"
	TypeStopLimit OrderType = "stop_limit"
	TypePostOnly  OrderType = "post_only"
	TypeIOC       OrderType = "ioc"
	TypeFOK       OrderType = "fok"
)

// OrderStatus 订单状态（与 t_orders.c_status / proto OrderStatus 对齐）。
type OrderStatus int

const (
	StatusPending         OrderStatus = 0 // 待提交
	StatusSubmitted       OrderStatus = 1 // 已提交
	StatusPartiallyFilled OrderStatus = 2 // 部分成交
	StatusFilled          OrderStatus = 3 // 完全成交
	StatusCanceled        OrderStatus = 4 // 已撤销
	StatusPartialCanceled OrderStatus = 5 // 部分成交后撤销
	StatusRejected        OrderStatus = 6 // 拒绝
	StatusExpired         OrderStatus = 7 // 过期
)

// Credential 交易所 API 凭证（由 t_account_api_keys 解密后传入）。
type Credential struct {
	APIKey     string
	APISecret  string
	Passphrase string // OKX 需要
}

// Instrument 交易规则（精度/最小下单量等），下单前本地校验用。
type Instrument struct {
	Symbol      string // 统一交易对，如 BTC-USDT
	Market      MarketType
	BaseCcy     string
	QuoteCcy    string
	TickSize    string // 价格精度
	LotSize     string // 数量精度
	MinNotional string // 最小名义价值
	MinQty      string // 最小下单量
	Status      string // 交易对状态
}

// AccountInfo 账户概览。
type AccountInfo struct {
	Market    MarketType
	TotalEq   string // 总权益（计价币）
	Available string // 可用
	Frozen    string // 冻结
	Raw       map[string]string
}

// Balance 单币种余额。
type Balance struct {
	Currency  string
	Available string
	Frozen    string
	Total     string
}

// FeeRate 手续费率。
type FeeRate struct {
	Symbol string
	Maker  string
	Taker  string
}

// FundFlow 资金流水。
type FundFlow struct {
	FlowID    string
	Currency  string
	BizType   string // deposit/withdraw/transfer/trade/fee/funding/...
	Direction int    // 1=增加, -1=减少
	Amount    string
	Balance   string // 变动后余额
	Timestamp int64
}

// Order 订单（适配层中立模型）。
type Order struct {
	OrderID         string // 系统/客户端订单号
	ClientOrderID   string
	ExchangeOrderID string
	Symbol          string
	Market          MarketType
	Side            OrderSide
	PosSide         string // long/short/net
	Type            OrderType
	Price           string
	Quantity        string
	FilledQty       string
	FilledAmount    string
	AvgPrice        string
	Fee             string
	FeeCurrency     string
	Status          OrderStatus
	RejectReason    string
	CreatedAt       int64
	UpdatedAt       int64
}

// OrderResult 下单/撤单/改单的轻量返回。
type OrderResult struct {
	OrderID         string
	ClientOrderID   string
	ExchangeOrderID string
	Status          OrderStatus
}

// Trade 成交明细。
type Trade struct {
	TradeID         string
	ExchangeTradeID string
	OrderID         string
	Symbol          string
	Side            OrderSide
	Price           string
	Quantity        string
	Amount          string
	Fee             string
	FeeCurrency     string
	Role            string // maker/taker
	TradedAt        int64
}

// Position 持仓。
type Position struct {
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

// ===== 请求参数 =====

// TransferReq 划转请求。
type TransferReq struct {
	Currency string
	Amount   string
	From     MarketType
	To       MarketType
	Remark   string
}

// TransferResult 划转结果。
type TransferResult struct {
	TransferID string
}

// FundFlowQuery 资金流水查询。
type FundFlowQuery struct {
	Market    MarketType
	Currency  string
	BizType   string
	StartTime int64
	EndTime   int64
	Limit     int
}

// PlaceOrderReq 下单请求。
type PlaceOrderReq struct {
	Market        MarketType
	Symbol        string
	Side          OrderSide
	PosSide       string
	Type          OrderType
	TimeInForce   string
	Price         string
	Quantity      string
	Amount        string // 市价买单按金额下单
	ClientOrderID string
	ReduceOnly    bool
	TriggerPrice  string
}

// CancelOrderReq 撤单请求（OrderID 与 ClientOrderID 二选一）。
type CancelOrderReq struct {
	Market        MarketType
	Symbol        string
	OrderID       string
	ClientOrderID string
}

// AmendOrderReq 改单请求。
type AmendOrderReq struct {
	Market        MarketType
	Symbol        string
	OrderID       string
	ClientOrderID string
	NewPrice      string // 可空
	NewQuantity   string // 可空
}

// GetOrderReq 单订单查询。
type GetOrderReq struct {
	Market        MarketType
	Symbol        string
	OrderID       string
	ClientOrderID string
}

// ListOrdersReq 订单列表查询。
type ListOrdersReq struct {
	Market    MarketType
	Symbol    string
	OnlyOpen  bool
	StartTime int64
	EndTime   int64
	Limit     int
}

// ListTradesReq 成交列表查询。
type ListTradesReq struct {
	Market    MarketType
	Symbol    string
	OrderID   string
	StartTime int64
	EndTime   int64
	Limit     int
}

// ===== 私有 WebSocket 回报事件 =====

// OrderEvent 订单更新事件。
type OrderEvent struct {
	Order Order
}

// TradeEvent 成交事件。
type TradeEvent struct {
	Trade Trade
}

// PositionEvent 持仓更新事件。
type PositionEvent struct {
	Position Position
}

// BalanceEvent 余额变更事件。
type BalanceEvent struct {
	Balances []Balance
}
