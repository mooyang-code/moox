package recalcexec

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/stretchr/testify/require"
)

func interval(alias string, start, end time.Time, window float64, source string) domain.ParamInterval {
	snapshot := domain.ParamSnapshot{"window": {Default: window}}
	return domain.ParamInterval{MemberAlias: alias, Start: start, End: end, Snapshot: snapshot, SnapshotHash: snapshot.Hash(), Source: source}
}

// 补算按冻结区间分参数段，段内每期每个成员的快照相同：区间之前没有记录的期次用默认参数（不放进计划），
// 区间之间的空档沿用之前最近的区间（来源记为 carried；默认值沿用仍是 default），最后一个区间之后同样沿用。
func TestRunChunkSplitsByParamIntervals(t *testing.T) {
	window := testWindow(10) // 00:00～00:10
	at := func(minute int) time.Time { return window.Start.Add(time.Duration(minute) * time.Minute) }
	selection := executorSelection("BTC")
	alias := selection.Members[0].Alias
	selection.Intervals = map[string][]domain.ParamInterval{alias: {
		interval(alias, at(2), at(3), 30, domain.ParamsRequested),
		interval(alias, at(6), at(6), 40, domain.ParamsRequested),
	}}
	runner := &recordingRunner{}
	executor := NewExecutor(runner, WithClock(periodclock.Continuous{}), WithChunkPeriods(100))

	_, err := executor.RunChunk(context.Background(), selection, window.Start, window.End)
	require.NoError(t, err)

	type segment struct {
		start, end int
		window     float64
		source     string
	}
	var got []segment
	for _, plan := range runner.snapshot() {
		item := segment{start: int(plan.TargetStart.Sub(window.Start) / time.Minute), end: int(plan.TargetEnd.Sub(window.Start) / time.Minute)}
		if freeze, ok := plan.Params[alias]; ok {
			item.window, item.source = freeze.Snapshot.Value("window", "BTC"), freeze.Source
		}
		got = append(got, item)
	}
	require.Equal(t, []segment{
		{0, 2, 0, ""},
		{2, 4, 30, domain.ParamsRequested},
		{4, 6, 30, domain.ParamsCarried},
		{6, 7, 40, domain.ParamsRequested},
		{7, 10, 40, domain.ParamsCarried},
	}, got)
}

// 沿用默认值区间的空档仍记为 default：从来没有提交过参数的成员不会因为补算变成 carried。
func TestParamSegmentCarriedFromDefaultStaysDefault(t *testing.T) {
	window := testWindow(4)
	selection := executorSelection("BTC")
	alias := selection.Members[0].Alias
	selection.Intervals = map[string][]domain.ParamInterval{alias: {interval(alias, window.Start, window.Start, 20, domain.ParamsDefault)}}
	end, params := paramSegment(selection, window.Start.Add(time.Minute), window.End, time.Minute)
	require.Equal(t, window.End, end)
	require.Equal(t, domain.ParamsDefault, params[alias].Source)
}
