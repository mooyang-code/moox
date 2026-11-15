package factorwire

import (
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
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
