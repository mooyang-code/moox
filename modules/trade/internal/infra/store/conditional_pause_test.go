package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// 条件暂停按暂停来源与减仓状态比对：组合已不是预期的状态（用户恢复了、人工暂停了）时不改写。
func TestPauseLogicalAccountIfOnlyRewritesExpectedState(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.CreateLogicalAccount(LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "one", Environment: "PAPER", SettlementAsset: "USDT",
			PauseReason: "新建",
		})
	}))
	pauseIf := func(expected, pause Pause) bool {
		var changed bool
		require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
			var err error
			changed, err = tx.PauseLogicalAccountIf("space-1", "logical-1", expected, pause)
			return err
		}))
		return changed
	}
	current := func() LogicalAccountRecord {
		logical, err := s.GetLogicalAccount(ctx, "space-1", "logical-1")
		require.NoError(t, err)
		return logical
	}
	require.Equal(t, PauseSourceUser, current().PauseSource, "新建组合的初始暂停记为人工")

	// 人工暂停之后，风控“减仓待执行 → 没有减仓”的改写不生效。
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.PauseLogicalAccount("space-1", "logical-1", UserPause("人工暂停"))
	}))
	require.False(t, pauseIf(MarginPause(MarginReducePending), MarginPause(MarginUnreduced)))
	require.Equal(t, UserPause("人工暂停"), current().Pause())

	// 预期仍成立时才改，原因跟着减仓状态换成固定文字。
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.PauseLogicalAccount("space-1", "logical-1", MarginPause(MarginReducePending))
	}))
	require.True(t, pauseIf(MarginPause(MarginReducePending), MarginPause(MarginUnreduced)))
	require.Equal(t, MarginPause(MarginUnreduced), current().Pause())

	// 组合已不在自动执行时，“运行中 → 减仓待执行”的暂停不生效。
	require.False(t, pauseIf(Pause{}, MarginPause(MarginReducePending)))

	// 恢复后三列一起清空，再从运行中暂停才生效。
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.ResumeLogicalAccount("space-1", "logical-1")
	}))
	require.True(t, current().IsActive())
	require.True(t, pauseIf(Pause{}, SystemPause("发现外部订单")))
	require.Equal(t, PauseSourceSystem, current().PauseSource)
	require.True(t, current().Pause().Auto())
	require.False(t, current().Pause().Margin())
}

// 表约束：暂停来源、原因与减仓状态必须自洽，自主下单不能写暂停列。
func TestLogicalAccountPauseColumnsConstraints(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.CreateLogicalAccount(LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "one", Environment: "PAPER", SettlementAsset: "USDT",
			PauseReason: "新建",
		})
	}))
	for _, update := range []string{
		`UPDATE t_logical_accounts SET c_pause_source = ''`,
		`UPDATE t_logical_accounts SET c_margin_reduce_state = 'PENDING'`,
		`UPDATE t_logical_accounts SET c_pause_source = 'MARGIN'`,
		`UPDATE t_logical_accounts SET c_pause_reason = ''`,
		`UPDATE t_logical_accounts SET c_pause_source = 'OTHER'`,
		`UPDATE t_logical_accounts SET c_control_mode = 'MANUAL'`,
	} {
		require.Error(t, s.DBForTest().Exec(update).Error, update)
	}
	require.Error(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.PauseLogicalAccount("space-1", "logical-1", Pause{Source: PauseSourceMargin, Reason: "缺减仓状态"})
	}), "保证金率风控暂停必须带减仓状态")
}
