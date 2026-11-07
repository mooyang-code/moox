package observability

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// 统一数据集：同一个标的的现货序列正常、永续序列落后时，监控能看出来；
// 永续落后不会被现货的最新一根盖住，名单里仍是标的名，水位取落后序列最老的一根。
func TestViewSubjectTrackerFlagsLaggingMarketOfUnifiedDataset(t *testing.T) {
	const spot, swap = "venue:binance|market:spot|source:spot_http", "venue:binance|market:swap|source:swap_http"
	registry := prometheus.NewRegistry()
	metrics, err := NewViewMetrics(registry)
	require.NoError(t, err)
	latest := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	observe := func(subject, tag string, at time.Time) {
		require.NoError(t, metrics.ObserveViewDatasetOutput(ViewDatasetObservation{
			SpaceID: "crypto", ViewID: "unified-view", DatasetID: "unified", SubjectID: subject, Frequency: "1m", SeriesTag: tag, DataTime: at,
		}))
	}
	// BTC：现货最新，永续落后 10 分钟（超过一根加容忍）；ETH 两个市场都最新。
	observe("BTC-USDT", spot, latest)
	observe("BTC-USDT", swap, latest.Add(-10*time.Minute))
	observe("ETH-USDT", spot, latest)
	observe("ETH-USDT", swap, latest)

	scope := map[string]string{"space_id": "crypto", "view_id": "unified-view", "dataset_id": "unified", "freq": "1m"}
	assertViewDatasetMetric(t, registry, "moox_storage_view_output_subjects", scope, 2)
	assertViewDatasetMetric(t, registry, "moox_storage_view_output_lagging_subjects", scope, 1)
	named := map[string]string{"space_id": "crypto", "view_id": "unified-view", "dataset_id": "unified", "freq": "1m", "subject_id": "BTC-USDT"}
	assertViewDatasetMetric(t, registry, "moox_storage_view_output_lagging_subject_data_time_seconds", named, float64(latest.Add(-10*time.Minute).Unix()))

	// 永续追上之后不再落后。
	observe("BTC-USDT", swap, latest)
	assertViewDatasetMetric(t, registry, "moox_storage_view_output_lagging_subjects", scope, 0)
}

// 单序列 View 的输出与按标的记录时完全一致；观察上限按序列计。
func TestViewSubjectTrackerBudgetCountsSeries(t *testing.T) {
	tracker := newViewSubjectTracker()
	at := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	observe := func(subject, tag string) error {
		return tracker.observe(ViewDatasetObservation{SpaceID: "s", ViewID: "v", DatasetID: "d", SubjectID: subject, Frequency: "1m", SeriesTag: tag, DataTime: at})
	}
	require.NoError(t, observe("A", "t1"))
	require.NoError(t, observe("A", "t2"))
	require.NoError(t, observe("A", "t2"), "同一序列重复观察不占额度")
	require.Equal(t, 2, tracker.observed)
	tracker.retain(map[ViewKey]struct{}{})
	require.Equal(t, 0, tracker.observed)
}

// 超过序列上限：只在第一次返回 ErrViewSeriesLimit，之后的新序列静默丢弃，已跟踪的序列照常更新；retain 释放额度后再超限会重新报告。
func TestViewSubjectTrackerLimitIsReportedOnce(t *testing.T) {
	tracker := newViewSubjectTracker()
	require.Equal(t, maxViewSeriesObservations, tracker.limit)
	tracker.limit = 2
	at := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	observe := func(view, subject, tag string, dataTime time.Time) error {
		return tracker.observe(ViewDatasetObservation{SpaceID: "s", ViewID: view, DatasetID: "d", SubjectID: subject, Frequency: "1m", SeriesTag: tag, DataTime: dataTime})
	}
	require.NoError(t, observe("v1", "A", "spot", at))
	require.NoError(t, observe("v1", "A", "swap", at))
	require.ErrorIs(t, observe("v2", "B", "spot", at), ErrViewSeriesLimit)
	for i := 0; i < 5; i++ {
		require.NoError(t, observe("v2", "C", "spot", at), "已报告过，之后的新序列静默丢弃")
	}
	require.NoError(t, observe("v1", "A", "spot", at.Add(time.Minute)), "已跟踪的序列照常更新")
	require.Equal(t, 2, tracker.observed)
	require.Equal(t, at.Add(time.Minute).Unix(), tracker.scopes[viewSubjectScope{spaceID: "s", viewID: "v1", datasetID: "d", freq: "1m"}].series[viewSeries{subjectID: "A", seriesTag: "spot"}])

	// v1 不再活跃：释放额度，v2 的新序列可以跟踪，再次超限时重新报告一次。
	tracker.retain(map[ViewKey]struct{}{{SpaceID: "s", ViewID: "v2"}: {}})
	require.NoError(t, observe("v2", "B", "spot", at))
	require.NoError(t, observe("v2", "B", "swap", at))
	require.ErrorIs(t, observe("v2", "C", "spot", at), ErrViewSeriesLimit)
	require.NoError(t, observe("v2", "D", "spot", at))
}
