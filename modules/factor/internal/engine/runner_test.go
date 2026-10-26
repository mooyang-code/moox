package engine

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/stretchr/testify/require"
)

func TestStageSummaryListsStagesInPipelineOrder(t *testing.T) {
	summary := stageSummary(map[string]time.Duration{
		"write": 1500 * time.Millisecond, "load": 2 * time.Second, "compute": 250 * time.Microsecond,
	})

	require.Equal(t, "load=2s compute=0s write=1.5s", summary)
}

func TestWithWarmupMarksUnseenTasksWarming(t *testing.T) {
	runners := withWarmup(
		[]domain.RunnerStatus{{TaskID: "a", Queued: 2, Active: true}},
		[]string{"a", "b"},
		[]pipeline.WarmupStatus{{TaskID: "a", State: pipeline.WarmupReady, WarmSubjects: 5, ExpectedSubjects: 5}},
	)
	require.Equal(t, []domain.RunnerStatus{
		{TaskID: "a", Queued: 2, Active: true, WarmupState: pipeline.WarmupReady, WarmSubjects: 5, ExpectedSubjects: 5},
		{TaskID: "b", WarmupState: pipeline.WarmupWarming},
	}, runners)
}

// 消费者收到任何数据集的周期事件都会建 runner（包括还没有因子任务的 1h K 线）：没有启用因子任务的 runner 不上报，
// 否则会一直显示 warming，因子总览的“数据预热”永远停在“预热中”。
func TestWithWarmupDropsRunnersWithoutAnEnabledTask(t *testing.T) {
	runners := withWarmup(
		[]domain.RunnerStatus{
			{TaskID: "ftask_bars_1m", Queued: 1, Active: true},
			{TaskID: "ftask_bars_1h", Queued: 0},
			{TaskID: "ftask_perp_1h"},
		},
		[]string{"ftask_bars_1m"},
		[]pipeline.WarmupStatus{{TaskID: "ftask_bars_1m", State: pipeline.WarmupReady, WarmSubjects: 3, ExpectedSubjects: 3}},
	)
	require.Equal(t, []domain.RunnerStatus{
		{TaskID: "ftask_bars_1m", Queued: 1, Active: true, WarmupState: pipeline.WarmupReady, WarmSubjects: 3, ExpectedSubjects: 3},
	}, runners)

	require.Empty(t, withWarmup([]domain.RunnerStatus{{TaskID: "orphan"}}, nil, nil), "没有任何启用的因子任务时不上报 runner")
}
