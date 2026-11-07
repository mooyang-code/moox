package marginmonitor

import (
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// 风控减仓失败或只完成一部分：暂停原因如实改写（不再写“已按比例减仓”），关注项详情带上减仓状态；
// 部分完成时现货卖出所得照常归集。
func TestReduceOutcomeIsReflectedInPauseReasonAndAttention(t *testing.T) {
	for status, want := range map[string]string{
		"FAILED":  store.MarginPauseReduceFailedReason(),
		"PARTIAL": store.MarginPauseReducePartialReason(),
	} {
		t.Run(status, func(t *testing.T) {
			f := newFixture(t, "", "400")
			f.run()
			require.Len(t, f.reduces, 1)
			actionID := f.reduces[0].actionID
			require.NoError(t, f.store.DBForTest().Exec(`UPDATE t_operator_actions SET c_status = ? WHERE c_action_id = ?`, status, actionID).Error)

			f.run()
			require.Equal(t, want, f.logical().PauseReason)
			require.True(t, store.IsMarginPauseReason(f.logical().PauseReason), "仍算保证金风控暂停")
			require.Contains(t, store.MarginAttentionDetail(f.logical().PauseReason), "人工处理")
			require.Len(t, f.collect, 1, "减仓结束后照常归集")

			f.run()
			require.Len(t, f.reduces, 1, "改写原因后不会再次自动减仓")
		})
	}
}

// 普通账户总权益填了但不大于 0（亏光、穿仓）是最危险的情况：有合约持仓就触发。
func TestEvaluateStandardAccountTriggersOnNonPositiveEquity(t *testing.T) {
	settings := defaults(t)
	for _, equity := range []string{"0", "-50"} {
		reading, ok := Evaluate(standardAccount(equity), []store.PositionRecord{position("10", "100")}, settings)
		require.True(t, ok, equity)
		require.True(t, reading.Triggered, equity)
	}
	_, ok := Evaluate(standardAccount("0"), nil, settings)
	require.False(t, ok, "没有持仓仍不触发")
}

// 没就绪的账户这一轮不判断，但快照太久没更新的告警照发。
func TestNotReadyAccountStillWarnsWhenSnapshotIsStale(t *testing.T) {
	f := newFixture(t, "", "400")
	require.NoError(t, f.store.DBForTest().Exec(`UPDATE t_trading_accounts SET c_ready = 0, c_last_sync_at = 1`).Error)
	f.run()
	require.Empty(t, f.reduces)
	f.service.mu.Lock()
	_, warned := f.service.warned["stale|account-1"]
	f.service.mu.Unlock()
	require.True(t, warned)
}
