package engine

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
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

type earliestStoreFake struct {
	storageio.Store
	at time.Time
}

func (s earliestStoreFake) EarliestPeriod(context.Context, string, string, string, []string, []string) (time.Time, bool, error) {
	return s.at, true, nil
}

// observedStore 嵌入的是 storageio.Store 接口，必须显式转发 EarliestPeriod，否则窗口空洞检查拿不到首根 K 线。
func TestObservedStoreForwardsEarliestPeriod(t *testing.T) {
	at := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	reader, ok := any(&observedStore{Store: earliestStoreFake{at: at}}).(earliestPeriodReader)
	require.True(t, ok)
	got, found, err := reader.EarliestPeriod(context.Background(), "crypto", "bars", "1m", []string{"BTC"}, []string{"close"})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, at, got)

	_, _, err = (&observedStore{Store: nil}).EarliestPeriod(context.Background(), "crypto", "bars", "1m", []string{"BTC"}, []string{"close"})
	require.Error(t, err, "底层不支持时返回错误（pipeline 据此保守判空洞）")
}
