package exchange

import (
	"context"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

type Adapter interface {
	Exchange() Exchange
	LoadInstruments(context.Context) ([]Instrument, error)
	GetAccountSnapshot(context.Context) (AccountSnapshot, error)
	ListPositionSnapshots(context.Context) ([]Position, error)
	ListOpenOrders(context.Context) ([]Order, error)
	ListRecentFills(context.Context, string, string) ([]Fill, string, error)
	GetOrder(context.Context, string, string) (Order, error)
	PlaceOrder(context.Context, OrderRequest) (Order, error)
	CancelOrder(context.Context, string, string) (Order, error)
	SetLeverage(context.Context, string, shared.Decimal) error
	SetMarginMode(context.Context, string, MarginMode) error
	SubscribePrivate(context.Context, EventHandler) error
}
