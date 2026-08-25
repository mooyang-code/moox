package execution

import (
	"context"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

type ExecutionAdapter interface {
	GetAccountSnapshot(context.Context) (exchange.AccountSnapshot, error)
	ListPositionSnapshots(context.Context) ([]exchange.Position, error)
	ListOpenOrders(context.Context) ([]exchange.Order, error)
	ListRecentFills(context.Context, shared.ExchangeSymbol, string) ([]exchange.Fill, string, error)
	GetOrder(context.Context, shared.ExchangeSymbol, string) (exchange.Order, error)
	PlaceOrder(context.Context, exchange.OrderRequest) (exchange.Order, error)
	CancelOrder(context.Context, shared.ExchangeSymbol, string) (exchange.Order, error)
	SetLeverage(context.Context, shared.ExchangeSymbol, shared.Decimal) error
	SetMarginMode(context.Context, shared.ExchangeSymbol, exchange.MarginMode) error
}
