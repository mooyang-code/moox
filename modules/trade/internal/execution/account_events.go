package execution

import (
	"context"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

type AccountEventSource interface {
	Subscribe(context.Context, AccountEventHandler) error
}

// AccountEventsOf 返回适配器真实具备的私有流：没有实现或 CapabilityProbe 报告不支持时返回 nil，
// 会话改为短周期同步，不会一个劲地订阅失败重连。
func AccountEventsOf(adapter any) AccountEventSource {
	if !Supports(adapter, CapabilityAccountEvents) {
		return nil
	}
	return adapter.(AccountEventSource)
}

type AccountEventHandler interface {
	OnSubscribed()
	OnOrder(context.Context, exchange.Order) error
	OnFill(context.Context, exchange.Fill) error
	OnPosition(context.Context, exchange.Position) error
	OnAccountSnapshot(context.Context, exchange.AccountSnapshot) error
}
