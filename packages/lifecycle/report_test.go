package lifecycle

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 上报给 Monitor 的生命周期：三态、暂停类型与原因、暂停起始时间、追赶进度（是否追平、目标、已完成、被什么挡住）。
func TestStateReportCarriesPauseAndCatchup(t *testing.T) {
	since := time.Date(2026, 11, 15, 1, 0, 0, 0, time.FixedZone("CST", 8*3600))
	state := State{Stage: StageResuming, Reason: "恢复追赶",
		Pause:   &PauseInfo{Kind: PauseSystem, Reason: "切换行情源", By: "cli:control", Since: since},
		Catchup: &Catchup{Since: since, Target: since.Add(time.Hour), Done: 2, Total: 5, BlockedBy: "上游", Unfillable: 1, Message: "补采中"}}
	got := state.Report()
	require.Equal(t, "resuming", got.GetStage())
	require.Equal(t, "system", got.GetPauseKind())
	require.Equal(t, "切换行情源", got.GetPauseReason())
	require.Equal(t, "cli:control", got.GetPausedBy())
	require.Equal(t, "2026-11-14T17:00:00Z", got.GetPausedSince(), "时间统一写成 UTC 的 RFC3339")
	require.Equal(t, "2026-11-14T18:00:00Z", got.GetCatchup().GetTarget())
	require.Equal(t, int64(5), got.GetCatchup().GetTotal())
	require.False(t, got.GetCatchup().GetCaughtUp())

	running := State{Stage: StageRunning}.Report()
	require.Empty(t, running.GetPauseKind())
	require.Nil(t, running.GetCatchup())
	require.Nil(t, (*Controller)(nil).ReportState(), "没有控制器时不带生命周期")
}
