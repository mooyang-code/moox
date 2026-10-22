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

func TestWithWarmupMarksUnseenSetsWarming(t *testing.T) {
	lanes := withWarmup(
		[]domain.LaneStatus{{SetID: "a", Queued: 2, Active: true}},
		[]string{"a", "b"},
		[]pipeline.WarmupStatus{{SetID: "a", State: pipeline.WarmupReady, WarmSubjects: 5, ExpectedSubjects: 5}},
	)
	require.Equal(t, []domain.LaneStatus{
		{SetID: "a", Queued: 2, Active: true, WarmupState: pipeline.WarmupReady, WarmSubjects: 5, ExpectedSubjects: 5},
		{SetID: "b", WarmupState: pipeline.WarmupWarming},
	}, lanes)
}

// 消费者收到任何数据集的周期事件都会建 lane（包括还没有因子集的 1h K 线）：没有启用因子集的 lane 不上报，
// 否则会一直显示 warming，因子总览的“数据预热”永远停在“预热中”。
func TestWithWarmupDropsLanesWithoutAnEnabledSet(t *testing.T) {
	lanes := withWarmup(
		[]domain.LaneStatus{
			{SetID: "fset_bars_1m", Queued: 1, Active: true},
			{SetID: "fset_bars_1h", Queued: 0},
			{SetID: "fset_perp_1h"},
		},
		[]string{"fset_bars_1m"},
		[]pipeline.WarmupStatus{{SetID: "fset_bars_1m", State: pipeline.WarmupReady, WarmSubjects: 3, ExpectedSubjects: 3}},
	)
	require.Equal(t, []domain.LaneStatus{
		{SetID: "fset_bars_1m", Queued: 1, Active: true, WarmupState: pipeline.WarmupReady, WarmSubjects: 3, ExpectedSubjects: 3},
	}, lanes)

	require.Empty(t, withWarmup([]domain.LaneStatus{{SetID: "orphan"}}, nil, nil), "没有任何启用的因子集时不上报 lane")
}
