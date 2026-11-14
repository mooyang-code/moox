package report

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// manualTimer 记下安排的补报，由测试手动触发，不依赖真实时间。
type manualTimer struct {
	delays []time.Duration
	runs   []func()
}

func (m *manualTimer) after(delay time.Duration, run func()) {
	m.delays = append(m.delays, delay)
	m.runs = append(m.runs, run)
}

func subscribedHandler(t *testing.T) (*Handler, *fakePublisher, *manualTimer) {
	t.Helper()
	publisher := &fakePublisher{}
	h, err := NewHandlerWithPublisher(validConfig("moox-storage"), publisher, prometheus.NewRegistry())
	require.NoError(t, err)
	timer := &manualTimer{}
	h.afterFunc = timer.after
	t.Cleanup(h.FlushOnOutputAdvance())
	return h, publisher, timer
}

func TestOutputAdvanceFlushesOnceWithinTheWindow(t *testing.T) {
	_, publisher, timer := subscribedHandler(t)

	// 同一时刻写完的几个数据集合并成一次补报，1 秒后发出。
	NotifyOutputAdvanced()
	NotifyOutputAdvanced()
	NotifyOutputAdvanced()
	require.Equal(t, []time.Duration{time.Second}, timer.delays)
	timer.runs[0]()
	require.Len(t, publisher.events, 1)

	// 补报之后再前进，再安排一次。
	NotifyOutputAdvanced()
	require.Len(t, timer.runs, 2)
}

func TestOutputAdvanceRetriesWhileTheTimerReportIsInFlight(t *testing.T) {
	h, publisher, timer := subscribedHandler(t)
	NotifyOutputAdvanced()
	// 定时上报正在进行：它的快照可能早于这次前进，补报稍后再试一次。
	h.inFlight.Store(true)
	timer.runs[0]()
	require.Empty(t, publisher.events)
	require.Len(t, timer.runs, 2)
	h.inFlight.Store(false)
	timer.runs[1]()
	require.Len(t, publisher.events, 1)
}

func TestOnlySubscribedHandlersFlush(t *testing.T) {
	_, _, timer := subscribedHandler(t)
	other, err := NewHandlerWithPublisher(validConfig("moox-other"), &fakePublisher{}, prometheus.NewRegistry())
	require.NoError(t, err)
	otherTimer := &manualTimer{}
	other.afterFunc = otherTimer.after
	cancel := other.FlushOnOutputAdvance()
	cancel()
	NotifyOutputAdvanced()
	require.Len(t, timer.runs, 1)
	require.Empty(t, otherTimer.runs, "取消订阅后不再补报")
}

func TestDatasetMetricsRequestFlushOnlyWhenTheOutputAdvances(t *testing.T) {
	_, _, timer := subscribedHandler(t)
	metrics, err := NewDatasetMetrics(prometheus.NewRegistry(), "storage")
	require.NoError(t, err)
	key := DatasetKey{SpaceID: "crypto", DatasetID: "dataset_spot_kline_1m", Freq: "1m"}
	require.NoError(t, metrics.ReplaceExpected([]DatasetExpectation{{Key: key, Interval: time.Minute}}))
	bar := time.Date(2026, 10, 16, 18, 54, 0, 0, time.UTC)
	observe := func(watermark time.Time) {
		require.NoError(t, metrics.ObserveRun(DatasetObservation{Key: key, Result: "success", Rows: 1, FinishedAt: watermark.Add(time.Minute), OutputWatermark: watermark}))
	}

	observe(bar)
	require.Len(t, timer.runs, 1)
	timer.runs[0]()
	// 水位没有前进（同一根重复写入）：不补报。
	observe(bar)
	require.Len(t, timer.runs, 1)
	observe(bar.Add(time.Minute))
	require.Len(t, timer.runs, 2)
}
