package operator

import (
	"context"
	"testing"

	operatordomain "github.com/mooyang-code/moox/modules/trade/internal/domain/operator"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/require"
)

func automationState(t *testing.T, f *operatorFixture) string {
	t.Helper()
	logical, err := f.store.GetLogicalAccount(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	return logical.AutomationState
}

func setControlMode(t *testing.T, f *operatorFixture, mode string) {
	t.Helper()
	require.NoError(t, f.store.DBForTest().Exec("UPDATE t_logical_accounts SET c_control_mode=? WHERE c_logical_account_id='logical-1'", mode).Error)
}

// 策略驱动的组合：唯一入口走接管下单（先暂停组合），和直接调 PlaceManualOrder 的结果一样。
func TestPlaceOrderTakesOverStrategyLogicalAccount(t *testing.T) {
	f := newOperatorFixture(t, exchange.MarketTypeSpot)
	require.Equal(t, "ACTIVE", automationState(t, f))

	result, err := f.service().PlaceOrder(context.Background(), submissionCommand(f))

	require.NoError(t, err)
	require.Equal(t, string(operatordomain.ActionManualOrder), result.Action.ActionType)
	require.Equal(t, "PAUSED", automationState(t, f), "策略驱动的组合下单前先暂停")

	direct := newOperatorFixture(t, exchange.MarketTypeSpot)
	want, err := direct.service().PlaceManualOrder(context.Background(), submissionCommand(direct).ManualOrderCommand)
	require.NoError(t, err)
	require.Equal(t, want.Action.Status, result.Action.Status)
	require.Equal(t, want.Order.State, result.Order.State)
}

// 自主下单的组合：唯一入口走普通受理，不暂停组合，和直接调 SubmitOrder 的结果一样。
func TestPlaceOrderSubmitsOnManualLogicalAccount(t *testing.T) {
	f, s, adapter := realSubmissionService(t)
	before := automationState(t, f)

	result, err := s.PlaceOrder(context.Background(), submissionCommand(f))

	require.NoError(t, err)
	require.Equal(t, string(operatordomain.ActionSubmitOrder), result.Action.ActionType)
	require.Equal(t, string(operatordomain.StatusRunning), result.Action.Status)
	require.Equal(t, before, automationState(t, f), "自主下单不改组合的暂停状态")
	require.Empty(t, f.trace, "不同步、不撤别的订单")
	require.Zero(t, adapter.posts, "只在本地受理，交易所提交由后台完成")
}

// 同一个 action_id 重试沿用第一次受理的流程，期间控制模式变了也不换。
func TestPlaceOrderRetryKeepsFirstFlow(t *testing.T) {
	t.Run("接管下单之后组合改成自主下单", func(t *testing.T) {
		f := newOperatorFixture(t, exchange.MarketTypeSpot)
		first, err := f.service().PlaceOrder(context.Background(), submissionCommand(f))
		require.NoError(t, err)
		setControlMode(t, f, "MANUAL")

		replay, err := f.service().PlaceOrder(context.Background(), submissionCommand(f))

		require.NoError(t, err)
		require.Equal(t, string(operatordomain.ActionManualOrder), replay.Action.ActionType)
		require.Equal(t, first.Order.OrderID, replay.Order.OrderID)
	})
	t.Run("普通受理之后组合改成策略驱动", func(t *testing.T) {
		f, s, _ := realSubmissionService(t)
		first, err := s.PlaceOrder(context.Background(), submissionCommand(f))
		require.NoError(t, err)
		before := automationState(t, f)
		// 重试按已有动作的类型走，不按现在的控制模式重新选。
		setControlMode(t, f, "STRATEGY")

		replay, _ := s.PlaceOrder(context.Background(), submissionCommand(f))

		require.Equal(t, string(operatordomain.ActionSubmitOrder), replay.Action.ActionType)
		require.Equal(t, first.Action.ActionID, replay.Action.ActionID)
		require.Equal(t, before, automationState(t, f), "重试不能改走接管流程把组合暂停")
	})
}

// 执行账户不在请求的组合里：不下单，也不暂停账户实际所在的组合。
func TestPlaceOrderRejectsAccountOutsideRequestedLogicalAccount(t *testing.T) {
	f := newOperatorFixture(t, exchange.MarketTypeSpot)
	command := submissionCommand(f)
	command.LogicalAccountID = "logical-other"

	_, err := f.service().PlaceOrder(context.Background(), command)

	require.ErrorIs(t, err, ErrInvalidCommand)
	require.Equal(t, "ACTIVE", automationState(t, f))
	_, getErr := f.store.GetOperatorAction(context.Background(), "space-1", command.ActionID)
	require.Error(t, getErr, "没有留下操作记录")
}
