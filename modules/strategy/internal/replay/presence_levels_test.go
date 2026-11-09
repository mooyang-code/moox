package replay

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/stretchr/testify/require"
)

// 年龄通过记录按 N 分开：通过 20 根判定的标的之后永远不再算“年龄小于 20”（max_age_bars 不会因窗口缺口把老标的误判成新标的），
// 但它在 5 根判定上要单独判定，不能借 20 根的记录。
func TestPresencePassedIsPerLevel(t *testing.T) {
	p := newPresence()
	p.indexes["A"] = map[int64]struct{}{81: {}, 96: {}}
	// bar 100：20 根的目标是 81，A 通过；5 根的目标是 96，A 也通过。
	got, err := p.probe(20, input.PeriodBoundaries{BarIndex: 100})(context.Background(), []string{"A"})
	require.NoError(t, err)
	require.Contains(t, got, "A")
	require.Contains(t, p.passed[20], "A")
	require.NotContains(t, p.passed[5], "A")
	// bar 130：窗口里都没有行。20 根已通过，仍算通过；5 根没有记录，重新判定，整窗为空是历史缺口。
	got, err = p.probe(20, input.PeriodBoundaries{BarIndex: 130})(context.Background(), []string{"A"})
	require.NoError(t, err)
	require.Contains(t, got, "A")
	_, err = p.probe(5, input.PeriodBoundaries{BarIndex: 130})(context.Background(), []string{"A"})
	var skip *input.SkipError
	require.ErrorAs(t, err, &skip)
}

// 年龄判定的读取范围：每个 N 各一个窄窗口 [首根 − (N+1), 末根 − (N−1)]，相差很大的 N 不读中间的几千根；相接的窗口合并。
func TestAgeWindowsPerLevel(t *testing.T) {
	at := func(hour int) time.Time {
		return time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(hour) * time.Hour)
	}
	segment := make([]input.PeriodBoundaries, 0, 5)
	for hour := 10000; hour < 10005; hour++ {
		bar, err := input.FromStorageStart(input.DefaultCalendar, "1h", at(hour))
		require.NoError(t, err)
		segment = append(segment, bar)
	}
	resolved := input.Resolved{Calendar: input.DefaultCalendar, Bar: "1h", MinAgeBars: 24, RuleAges: map[string]input.AgeRule{"a": {MinBars: 24}, "b": {MaxBars: 8760}}}
	windows, err := ageWindows(resolved, segment)
	require.NoError(t, err)
	require.Equal(t, []ageWindow{
		{From: at(10000 - 8761), To: at(10004 - 8759).Add(time.Nanosecond)},
		{From: at(10000 - 25), To: at(10004 - 23).Add(time.Nanosecond)},
	}, windows)
	// 相邻的 N（24 与 25）窗口相接，合并成一段。
	resolved.RuleAges = map[string]input.AgeRule{"a": {MinBars: 24}, "b": {MaxBars: 25}}
	windows, err = ageWindows(resolved, segment)
	require.NoError(t, err)
	require.Equal(t, []ageWindow{{From: at(10000 - 26), To: at(10004 - 23).Add(time.Nanosecond)}}, windows)
}
