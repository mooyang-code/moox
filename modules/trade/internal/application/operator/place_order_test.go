package operator

import (
	"context"
	"encoding/json"
	"testing"

	operatordomain "github.com/mooyang-code/moox/modules/trade/internal/domain/operator"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/require"
)

func automationState(t *testing.T, f *operatorFixture) string {
	t.Helper()
	logical, err := f.store.GetLogicalAccount(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	return logical.AutomationState()
}

func setControlMode(t *testing.T, f *operatorFixture, mode string) {
	t.Helper()
	if mode == "MANUAL" {
		require.NoError(t, f.store.DBForTest().Exec("UPDATE t_logical_accounts SET c_control_mode=?, c_pause_source='', c_pause_reason='', c_margin_reduce_state='' WHERE c_logical_account_id='logical-1'", mode).Error)
		return
	}
	require.NoError(t, f.store.DBForTest().Exec("UPDATE t_logical_accounts SET c_control_mode=? WHERE c_logical_account_id='logical-1'", mode).Error)
}

// actionTakeover 读动作结果里落库的流程。
func actionTakeover(t *testing.T, result ManualOrderResult) bool {
	t.Helper()
	require.NotNil(t, result.Action.ResultJSON)
	var progress manualOrderActionResult
	require.NoError(t, json.Unmarshal([]byte(*result.Action.ResultJSON), &progress))
	return progress.Takeover
}

// 策略驱动的组合：走接管流程，先暂停组合，同步提交到交易所。
func TestPlaceOrderTakesOverStrategyLogicalAccount(t *testing.T) {
	f := newOperatorFixture(t, exchange.MarketTypeSpot)
	require.Equal(t, "ACTIVE", automationState(t, f))

	result, err := f.service().PlaceOrder(context.Background(), submissionCommand(f))

	require.NoError(t, err)
	require.Equal(t, string(operatordomain.ActionManualOrder), result.Action.ActionType)
	require.True(t, actionTakeover(t, result))
	require.Equal(t, "PAUSED", automationState(t, f), "策略驱动的组合下单前先暂停")
	require.Equal(t, string(operatordomain.StatusCompleted), result.Action.Status)
	require.Equal(t, 1, f.orders.submitCalls, "接管流程在请求里就提交到交易所")
}

// 自主下单的组合：同一个动作类型走直接下单，不暂停组合、不撤单，只在本地受理。
func TestPlaceOrderSubmitsOnManualLogicalAccount(t *testing.T) {
	f, s, adapter := realSubmissionService(t)
	before := automationState(t, f)

	result, err := s.PlaceOrder(context.Background(), submissionCommand(f))

	require.NoError(t, err)
	require.Equal(t, string(operatordomain.ActionManualOrder), result.Action.ActionType)
	require.False(t, actionTakeover(t, result))
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
		require.True(t, actionTakeover(t, replay))
		require.Equal(t, first.Order.OrderID, replay.Order.OrderID)
	})
	t.Run("直接下单之后组合改成策略驱动", func(t *testing.T) {
		f, s, _ := realSubmissionService(t)
		first, err := s.PlaceOrder(context.Background(), submissionCommand(f))
		require.NoError(t, err)
		// 重试按已落库的流程走，不按现在的控制模式重新选。
		setControlMode(t, f, "STRATEGY")
		before := automationState(t, f)

		replay, _ := s.PlaceOrder(context.Background(), submissionCommand(f))

		require.False(t, actionTakeover(t, replay))
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
