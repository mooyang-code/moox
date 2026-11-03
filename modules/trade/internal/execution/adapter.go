package execution

import (
	"context"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

// ExecutionAdapter 是每个交易场所（交易所或券商）必须实现的接口。核心流程只依赖它，不做类型断言。
// 按需实现的能力见 AccountEventSource、SymbolConfigurer。
type ExecutionAdapter interface {
	MarketDataSource
	ReferencePriceSource
	ExchangeOrderLookup
	GetAccountSnapshot(context.Context) (exchange.AccountSnapshot, error)
	ListPositionSnapshots(context.Context) ([]exchange.Position, error)
	ListOpenOrders(context.Context) ([]exchange.Order, error)
	ListRecentFills(context.Context, shared.ExchangeSymbol, string) ([]exchange.Fill, string, error)
	GetOrder(context.Context, shared.ExchangeSymbol, string) (exchange.Order, error)
	PlaceOrder(context.Context, exchange.OrderRequest) (exchange.Order, error)
	CancelOrder(context.Context, shared.ExchangeSymbol, string) (exchange.Order, error)
}

// ExchangeOrderLookup 按交易所订单号查询订单。外部订单可能没有客户端订单号。
type ExchangeOrderLookup interface {
	GetOrderByExchangeID(context.Context, string, string) (exchange.Order, error)
}

// SymbolConfigurer 是需要按标的设置杠杆和保证金模式的场所才实现的能力。
type SymbolConfigurer interface {
	SetLeverage(context.Context, shared.ExchangeSymbol, shared.Decimal) error
	SetMarginMode(context.Context, shared.ExchangeSymbol, exchange.MarginMode) error
}
