package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// 条件暂停：组合当前的状态和原因已不是预期值（用户恢复了、人工改了原因）时不改写。
func TestPauseLogicalAccountIfOnlyRewritesExpectedReason(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.CreateLogicalAccount(LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "one", ExecutionMode: "PAPER", SettlementAsset: "USDT",
			AutomationState: "PAUSED", PauseReason: MarginPausePendingReason(),
		})
	}))
	pauseIf := func(state, expected, reason string) bool {
		var changed bool
		require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
			var err error
			changed, err = tx.PauseLogicalAccountIf("space-1", "logical-1", state, expected, reason)
			return err
		}))
		return changed
	}
	// 人工改写了原因之后，风控“减仓待执行 → 没有减仓”的改写不生效。
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.SetLogicalAccountAutomation("space-1", "logical-1", "PAUSED", "人工暂停")
	}))
	require.False(t, pauseIf("PAUSED", MarginPausePendingReason(), MarginPauseUnreducedReason()))
	logical, err := s.GetLogicalAccount(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "人工暂停", logical.PauseReason)
	// 预期仍成立时才改。
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.SetLogicalAccountAutomation("space-1", "logical-1", "PAUSED", MarginPausePendingReason())
	}))
	require.True(t, pauseIf("PAUSED", MarginPausePendingReason(), MarginPauseUnreducedReason()))
	logical, err = s.GetLogicalAccount(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, MarginPauseUnreducedReason(), logical.PauseReason)
	// 组合已不是自动执行状态时，“ACTIVE → 减仓待执行”的暂停不生效。
	require.False(t, pauseIf("ACTIVE", "", MarginPausePendingReason()))
}
