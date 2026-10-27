package factorwire

import (
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/stretchr/testify/require"
)

// 引擎心跳里的任务暂停标记经管理端协议往返不丢。
func TestRunnerStatusKeepsThePausedFlagOnTheWire(t *testing.T) {
	runners := []domain.RunnerStatus{
		{TaskID: "ftask_a", Queued: 2, Active: true, WarmupState: "ready", WarmSubjects: 5, ExpectedSubjects: 5},
		{TaskID: "ftask_b", Paused: true},
	}
	require.Equal(t, runners, RunnersFromPB(RunnersToPB(runners)))
}
