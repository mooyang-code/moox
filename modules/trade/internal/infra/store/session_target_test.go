package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 开始时刻更晚的会话的目标覆盖旧会话留下的目标行，不与它的 bar_end_time 比较（同一根、更早一根都覆盖）；
// 之后旧会话的目标按被取代拒绝。
func TestLaterSessionReplacesStaleTargetAtSameOrEarlierBar(t *testing.T) {
	for _, offset := range []time.Duration{0, -time.Second} {
		t.Run(offset.String(), func(t *testing.T) {
			s := openTestStore(t)
			ctx := context.Background()
			require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
				return tx.CreateLogicalAccount(LogicalAccountRecord{
					SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "main",
					ExecutionMode: "PAPER", SettlementAsset: "USDT",
					AutomationState: "PAUSED", PauseReason: "setup",
				})
			}))
			now := time.Now().UTC()
			bar := now.Add(-time.Second).UnixMilli()
			first := LogicalAccountTargetRecord{
				SpaceID: "space-1", LogicalAccountID: "logical-1", TargetID: "target-1",
				InstanceID: "instance-1", SessionID: "session-1", SessionStartedAt: 1_000, StrategyID: "strategy-1",
				BarEndTime: bar, ValidUntil: now.Add(time.Hour).UnixMilli(),
				Targets: []InstrumentQuantity{}, Status: "PENDING", AcceptedAt: now.UnixMilli(),
			}
			_, accepted, err := acceptTargetForTest(ctx, s, first)
			require.NoError(t, err)
			require.True(t, accepted)
			second := first
			second.TargetID, second.SessionID, second.SessionStartedAt = "target-2", "session-2", 2_000
			second.BarEndTime += offset.Milliseconds()
			_, accepted, err = acceptTargetForTest(ctx, s, second)
			require.NoError(t, err)
			require.True(t, accepted)
			_, accepted, err = acceptTargetForTest(ctx, s, first)
			require.ErrorIs(t, err, ErrTargetSuperseded)
			require.False(t, accepted)
			current, err := s.GetLogicalAccountTarget(ctx, "space-1", "logical-1")
			require.NoError(t, err)
			require.Equal(t, "target-2", current.TargetID)
			require.Equal(t, "session-2", current.SessionID)
			require.Equal(t, int64(2_000), current.SessionStartedAt)
		})
	}
}
