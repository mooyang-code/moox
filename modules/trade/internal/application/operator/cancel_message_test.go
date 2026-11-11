package operator

import (
	"context"
	"testing"

	operatordomain "github.com/mooyang-code/moox/modules/trade/internal/domain/operator"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/require"
)

// 撤单发出后同步还没看到订单结束：给用户的是中文说明，仍算 ErrCancelUnconfirmed，动作保持运行等下次确认。
func TestCancelOrderUnconfirmedUsesUserMessage(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSwap)
	fixture.order(t, activeOrder(fixture, "target-child", "TARGET"))
	fixture.orders.leaveOpen = map[string]bool{"target-child": true}

	result, err := fixture.service().CancelOrder(context.Background(), CancelOrderCommand{
		SpaceID: "space-1", ActionID: "cancel-1", OrderID: "target-child", Reason: "operator cancel",
	})

	require.ErrorIs(t, err, ErrCancelUnconfirmed)
	require.Equal(t, "撤单已提交，交易所还没确认，稍后刷新查看", err.Error())
	require.Equal(t, string(operatordomain.StatusRunning), result.Action.Status)
	require.Equal(t, "撤单已提交，交易所还没确认，稍后刷新查看", result.Action.LastError)
}
