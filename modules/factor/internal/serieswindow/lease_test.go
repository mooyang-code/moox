package serieswindow

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLeaseReleaseTracksHoldAndWarns(t *testing.T) {
	w, err := New(Config{Lookback: 4}, 60, []string{"close"})
	require.NoError(t, err)
	var warned []time.Duration
	w.SetLeaseWarn(10*time.Millisecond, func(held time.Duration) { warned = append(warned, held) })

	quick := w.Acquire(nil, 0)
	require.Equal(t, 1, w.Stats().Leases)
	quick.Release()
	quick.Release() // 可重复调用，只计一次
	require.Equal(t, 0, w.Stats().Leases)
	require.Empty(t, warned)

	slow := w.Acquire(nil, 0)
	time.Sleep(25 * time.Millisecond)
	slow.Release()
	require.Len(t, warned, 1)
	require.GreaterOrEqual(t, w.Stats().PeakLeaseHeld, 25*time.Millisecond)
	require.Equal(t, 0, w.Stats().Leases)

	var nilView *View
	nilView.Release() // 空视图、磁盘视图都不 panic
}

func TestRetainDropsSubjectsOutsideTheList(t *testing.T) {
	w, err := New(Config{Lookback: 4}, 60, []string{"close"})
	require.NoError(t, err)
	// 直接构造两个标的的序列。
	for _, subject := range []string{"A", "B"} {
		w.series[seriesKey{subject, ""}] = &series{cols: [][]float64{make([]float64, w.cap)}, filled: make([]bool, w.cap)}
		w.bySubject[subject] = []string{""}
	}
	require.Equal(t, 1, w.Retain([]string{"A"}))
	require.Equal(t, []string{"A"}, w.Subjects())
	require.Equal(t, 0, w.Retain([]string{"A"}))
}

func TestFilledRunsFindsLongestFilledRun(t *testing.T) {
	w, err := New(Config{Lookback: 6}, 60, []string{"close"})
	require.NoError(t, err)
	mk := func(filled ...bool) *series {
		s := &series{cols: [][]float64{make([]float64, w.cap)}, filled: make([]bool, w.cap), n: len(filled)}
		copy(s.filled, filled)
		return s
	}
	w.series[seriesKey{"A", ""}] = mk(false, true, true, true, false) // 窗口中间连续填充 3 根
	w.series[seriesKey{"B", ""}] = mk(true, false, true, false)       // 最长 1 根
	w.series[seriesKey{"C", ""}] = mk(false, false)                   // 没有填充
	for _, s := range []string{"A", "B", "C"} {
		w.bySubject[s] = []string{""}
	}
	require.Equal(t, map[string]int{"A": 3}, w.FilledRuns(2))
	require.Equal(t, map[string]int{"A": 3, "B": 1}, w.FilledRuns(1))
}
