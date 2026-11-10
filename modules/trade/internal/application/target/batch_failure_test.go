package target

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// 批量提交遇到其它错误（不是拒单、不是目标级冲突）：错误留给下一轮重试，目标状态和最近错误照常刷新。
func TestBatchOtherErrorStillRefreshesTargetState(t *testing.T) {
	f := swapPolicyFixture(t, legacyOrderSettings)
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "2"}})
	require.NoError(t, f.store.DBForTest().Exec(
		`UPDATE t_logical_account_targets SET c_last_error = '上一轮的旧错误' WHERE c_logical_account_id = 'logical-1'`).Error)
	submitFailed := errors.New("交易所暂时不可用")
	f.orders.submitErrors = map[string]error{"child-account-a": submitFailed}

	executor := f.executor()
	executor.MinBatchInterval = 0
	result, err := executor.Converge(context.Background(), "space-1", "logical-1")
	require.ErrorIs(t, err, submitFailed)
	require.Equal(t, StatusConverging, result.Status)
	require.Equal(t, "place", result.Action)
	target, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusConverging, target.Status, "状态照常刷新")
	require.Empty(t, target.LastError, "旧的最近错误被清掉")
}
