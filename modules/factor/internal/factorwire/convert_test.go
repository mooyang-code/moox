package factorwire

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/stretchr/testify/require"
)

// 引擎心跳里的任务暂停标记与计算前检查经管理端协议往返不丢。
func TestRunnerStatusKeepsThePausedFlagAndChecksOnTheWire(t *testing.T) {
	runners := []domain.RunnerStatus{
		{TaskID: "ftask_a", Queued: 2, Active: true, WarmupState: "ready", WarmSubjects: 5, ExpectedSubjects: 5},
		{TaskID: "ftask_b", Paused: true, Checks: []domain.TaskCheck{
			{Name: "本地窗口", Status: domain.TaskCheckFail, Message: "窗口还没有载入完成", Remedy: "等待自动载入"},
			{Name: "变更同步", Status: domain.TaskCheckPass},
		}},
	}
	require.Equal(t, runners, RunnersFromPB(RunnersToPB(runners)))
}

// 心跳里的引擎状态：就绪判断、暂停类型与原因、追赶进度往返不丢（factor-mgr 据此代报给 Monitor）。
func TestEngineStatusRoundTripsReadinessAndLifecycle(t *testing.T) {
	since := time.Date(2026, 11, 15, 1, 0, 0, 0, time.UTC)
	status := domain.EngineStatus{Ready: false, NotReadyReason: "lease conflict",
		Lifecycle: domain.EngineLifecycle{Stage: "resuming", PausedSince: since, PauseKind: "manual", PauseReason: "排查",
			Catchup: &lifecycle.Catchup{Since: since, Target: since.Add(time.Hour), Done: 3, Total: 4, BlockedBy: "任务 t1", Unfillable: 1, Message: "等待"}}}
	got := EngineStatusFromPB(EngineStatusToPB(status))
	if !got.Lifecycle.PausedSince.Equal(since) {
		t.Fatalf("paused_since = %v, want %v", got.Lifecycle.PausedSince, since)
	}
	if got.Ready != status.Ready || got.NotReadyReason != status.NotReadyReason || got.Lifecycle.PauseKind != "manual" || got.Lifecycle.PauseReason != "排查" {
		t.Fatalf("status = %+v", got)
	}
	if c := got.Lifecycle.Catchup; c == nil || *c != *status.Lifecycle.Catchup {
		t.Fatalf("catchup = %+v, want %+v", got.Lifecycle.Catchup, status.Lifecycle.Catchup)
	}
}

// 运行摘要的完成时刻在 RPC 里是 Unix 毫秒（不是秒）：往返后精确到毫秒，零值写 0 并还原成零时间。
func TestRunSummaryCompletedAtIsMillis(t *testing.T) {
	completed := time.Date(2026, 11, 24, 8, 0, 3, 456_000_000, time.UTC)
	pb := RunSummaryToPB(domain.TaskRunSummary{TaskID: "t", LastPeriodTime: 1786032000, CompletedAt: completed})
	if pb.GetCompletedAt() != completed.UnixMilli() || pb.GetLastPeriodTime() != 1786032000 {
		t.Fatalf("completed_at = %d, last_period_time = %d", pb.GetCompletedAt(), pb.GetLastPeriodTime())
	}
	if got := RunSummaryFromPB(pb); !got.CompletedAt.Equal(completed) {
		t.Fatalf("round trip completed_at = %v, want %v", got.CompletedAt, completed)
	}
	if unknown := RunSummaryToPB(domain.TaskRunSummary{TaskID: "t"}); unknown.GetCompletedAt() != 0 || !RunSummaryFromPB(unknown).CompletedAt.IsZero() {
		t.Fatalf("unknown completed_at = %d", unknown.GetCompletedAt())
	}
}
