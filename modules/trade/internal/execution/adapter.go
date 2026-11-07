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
	ListRecentFills(context.Context, exchange.SymbolKey, string) ([]exchange.Fill, string, error)
	GetOrder(context.Context, exchange.SymbolKey, string) (exchange.Order, error)
	PlaceOrder(context.Context, exchange.OrderRequest) (exchange.Order, error)
	CancelOrder(context.Context, exchange.SymbolKey, string) (exchange.Order, error)
}

// ExchangeOrderLookup 按交易所订单号查询订单。外部订单可能没有客户端订单号。
type ExchangeOrderLookup interface {
	GetOrderByExchangeID(context.Context, exchange.SymbolKey, string) (exchange.Order, error)
}

// LedgerSource 是能查询资金流水（手续费、资金费、已实现盈亏、划转等）的场所才实现的能力，只读、即查即用。
// 同一个适配器服务现货和永续时，LedgerSupported 按账户市场类型说明是否支持（例如币安现货没有统一流水接口）。
type LedgerSource interface {
	LedgerSupported() bool
	ListLedger(context.Context, exchange.LedgerQuery) (exchange.LedgerPage, error)
}

// SymbolConfigurer 是需要按标的设置杠杆和保证金模式的场所才实现的能力。
type SymbolConfigurer interface {
	SetLeverage(context.Context, exchange.SymbolKey, shared.Decimal) error
	SetMarginMode(context.Context, exchange.SymbolKey, exchange.MarginMode) error
}

// FeeDiscountConfigurer 是支持“用 BNB 抵扣手续费”开关的场所才实现的能力（币安）。
// 普通账户现货、合约各有一个开关，统一账户的合约和现货分别走不同接口；实现要把它们一起设置。
type FeeDiscountConfigurer interface {
	SetBNBFeeDiscount(ctx context.Context, enabled bool) error
	GetBNBFeeDiscount(ctx context.Context) (bool, error)
}

// DustConverter 是支持把小额资产兑换成 BNB 的场所才实现的能力（币安，现货或统一账户的杠杆钱包）。
type DustConverter interface {
	ListDust(ctx context.Context) ([]exchange.DustAsset, error)
	ConvertDust(ctx context.Context, assets []string) (exchange.DustConversion, error)
}
