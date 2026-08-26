package execution

import (
	"context"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

type AccountEventSource interface {
	Subscribe(context.Context, AccountEventHandler) error
}
type AccountEventHandler interface {
	OnSubscribed()
	OnOrder(context.Context, exchange.Order) error
	OnFill(context.Context, exchange.Fill) error
	OnPosition(context.Context, exchange.Position) error
	OnAccountSnapshot(context.Context, exchange.AccountSnapshot) error
}
