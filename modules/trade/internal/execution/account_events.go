package execution

import (
	"context"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

type AccountEventSource interface {
	Subscribe(context.Context, AccountEventHandler) error
}

// AccountEventsOf 返回适配器真实具备的私有流：没有实现，或合成适配器的某个子适配器没有私有流时返回 nil，
// 会话改为短周期同步，不会一个劲地订阅失败重连。
func AccountEventsOf(adapter ExecutionAdapter) AccountEventSource {
	source, ok := adapter.(AccountEventSource)
	if !ok {
		return nil
	}
	if capable, ok := adapter.(interface{ SupportsAccountEvents() bool }); ok && !capable.SupportsAccountEvents() {
		return nil
	}
	return source
}

type AccountEventHandler interface {
	OnSubscribed()
	OnOrder(context.Context, exchange.Order) error
	OnFill(context.Context, exchange.Fill) error
	OnPosition(context.Context, exchange.Position) error
	OnAccountSnapshot(context.Context, exchange.AccountSnapshot) error
}
