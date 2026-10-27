package report

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestDatasetMetricsNamesAndLabels(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewDatasetMetrics(registry, "collector")
	if err != nil {
		t.Fatal(err)
	}
	key := DatasetKey{SpaceID: "crypto", DatasetID: "market_kline", Freq: "1m"}
	if err := metrics.ReplaceExpected([]DatasetExpectation{{Key: key, Interval: time.Minute}}); err != nil {
		t.Fatal(err)
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	requireMetricFamily(t, families, "moox_collector_dataset_enabled")
	requireMetricFamily(t, families, "moox_collector_dataset_expected_interval_seconds")
	requireNoLabels(t, families, "module", "subject_id", "job_id", "error")
}

func TestDatasetMetricsReplaceExpectedIsAtomic(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewDatasetMetrics(registry, "factor")
	if err != nil {
		t.Fatal(err)
	}
	valid := DatasetExpectation{
		Key:      DatasetKey{SpaceID: "crypto", DatasetID: "factor_momentum", Freq: "1m"},
		Interval: time.Minute,
	}
	if err := metrics.ReplaceExpected([]DatasetExpectation{valid}); err != nil {
		t.Fatal(err)
	}
	if err := metrics.ReplaceExpected([]DatasetExpectation{{
		Key:      DatasetKey{SpaceID: "crypto", DatasetID: "bad dataset", Freq: "1m"},
		Interval: time.Minute,
	}}); err == nil {
		t.Fatal("invalid replacement was accepted")
	}
	if got := testutil.ToFloat64(metrics.enabled.WithLabelValues("crypto", "factor_momentum", "1m")); got != 1 {
		t.Fatalf("previous expected dataset was cleared: %v", got)
	}
}

func TestDatasetMetricsReplaceExpectedPublishesDisabledTombstone(t *testing.T) {
	metrics, err := NewDatasetMetrics(prometheus.NewRegistry(), "collector")
	if err != nil {
		t.Fatal(err)
	}
	key := DatasetKey{SpaceID: "crypto", DatasetID: "market_kline", Freq: "1m"}
	if err := metrics.ReplaceExpected([]DatasetExpectation{{Key: key, Interval: time.Minute}}); err != nil {
		t.Fatal(err)
	}
	if err := metrics.ReplaceExpected(nil); err != nil {
		t.Fatal(err)
	}
	labels := []string{"crypto", "market_kline", "1m"}
	if got := testutil.ToFloat64(metrics.enabled.WithLabelValues(labels...)); got != 0 {
		t.Fatalf("disabled tombstone = %v, want 0", got)
	}
	if got := testutil.ToFloat64(metrics.expectedInterval.WithLabelValues(labels...)); got != 0 {
		t.Fatalf("disabled interval = %v, want 0", got)
	}
}

// 暂停项：数据集在清单里但不被期望产出——enabled 为 0、不进 expected；进程重启后第一轮清单里也要有这条 0 序列
// （Monitor 靠它分辨“被有意停下”和“从没有过”），不要求 Interval。
func TestDatasetMetricsPausedExpectationPublishesDisabledSeries(t *testing.T) {
	metrics, err := NewDatasetMetrics(prometheus.NewRegistry(), "factor")
	if err != nil {
		t.Fatal(err)
	}
	live := DatasetKey{SpaceID: "crypto", DatasetID: "factor_live", Freq: "5m"}
	paused := DatasetKey{SpaceID: "crypto", DatasetID: "factor_paused", Freq: "1m"}
	if err := metrics.ReplaceExpected([]DatasetExpectation{{Key: live, Interval: 5 * time.Minute}, {Key: paused, Paused: true}}); err != nil {
		t.Fatal(err)
	}
	if got := testutil.ToFloat64(metrics.enabled.WithLabelValues("crypto", "factor_live", "5m")); got != 1 {
		t.Fatalf("live enabled = %v, want 1", got)
	}
	if got := testutil.ToFloat64(metrics.enabled.WithLabelValues("crypto", "factor_paused", "1m")); got != 0 {
		t.Fatalf("paused enabled = %v, want 0", got)
	}
	if _, expected := metrics.expected[paused]; expected {
		t.Fatal("a paused dataset must not be expected to run")
	}
	// 同一个数据集既暂停又正常是清单自相矛盾，整体拒绝。
	if err := metrics.ReplaceExpected([]DatasetExpectation{{Key: live, Interval: time.Minute}, {Key: live, Paused: true}}); err == nil {
		t.Fatal("conflicting paused and live expectation was accepted")
	}
	// 恢复后重新期望。
	if err := metrics.ReplaceExpected([]DatasetExpectation{{Key: live, Interval: 5 * time.Minute}, {Key: paused, Interval: time.Minute}}); err != nil {
		t.Fatal(err)
	}
	if got := testutil.ToFloat64(metrics.enabled.WithLabelValues("crypto", "factor_paused", "1m")); got != 1 {
		t.Fatalf("resumed enabled = %v, want 1", got)
	}
}

func TestDatasetMetricsRecordsUpstreamInventoryRefreshError(t *testing.T) {
	metrics, err := NewDatasetMetrics(prometheus.NewRegistry(), "factor")
	if err != nil {
		t.Fatal(err)
	}
	metrics.ObserveInventoryRefreshError()
	if got := testutil.ToFloat64(metrics.inventoryRefreshErrors); got != 1 {
		t.Fatalf("inventory refresh errors = %v, want 1", got)
	}
}

func TestDatasetMetricsObserveRunUsesMaxWatermarks(t *testing.T) {
	metrics, err := NewDatasetMetrics(prometheus.NewRegistry(), "collector")
	if err != nil {
		t.Fatal(err)
	}
	key := DatasetKey{SpaceID: "crypto", DatasetID: "market_kline", Freq: "1m"}
	if err := metrics.ReplaceExpected([]DatasetExpectation{{Key: key, Interval: time.Minute}}); err != nil {
		t.Fatal(err)
	}
	newer := time.Unix(200, 0)
	if err := metrics.ObserveRun(DatasetObservation{
		Key: key, Result: "success", Rows: 2, FinishedAt: time.Unix(210, 0),
		InputWatermark: newer, OutputWatermark: newer,
	}); err != nil {
		t.Fatal(err)
	}
	if err := metrics.ObserveRun(DatasetObservation{
		Key: key, Result: "success", Rows: 1, FinishedAt: time.Unix(220, 0),
		InputWatermark: newer.Add(-time.Minute), OutputWatermark: newer.Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	labels := []string{"crypto", "market_kline", "1m"}
	if got := testutil.ToFloat64(metrics.inputWatermark.WithLabelValues(labels...)); got != 200 {
		t.Fatalf("input watermark regressed to %v", got)
	}
	if got := testutil.ToFloat64(metrics.outputWatermark.WithLabelValues(labels...)); got != 200 {
		t.Fatalf("output watermark regressed to %v", got)
	}
	if got := testutil.ToFloat64(metrics.rows.WithLabelValues("crypto", "market_kline", "1m", "success")); got != 3 {
		t.Fatalf("rows total = %v, want 3", got)
	}
	// 到达时间只在输出水位前进时记录：第二次运行没有推进水位，仍是第一次写入完成的时刻。
	if got := testutil.ToFloat64(metrics.outputArrival.WithLabelValues(labels...)); got != 210 {
		t.Fatalf("output arrival = %v, want 210", got)
	}
	if err := metrics.ObserveRun(DatasetObservation{
		Key: key, Result: "success", Rows: 1, FinishedAt: time.Unix(275, 0), OutputWatermark: newer.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if got := testutil.ToFloat64(metrics.outputArrival.WithLabelValues(labels...)); got != 275 {
		t.Fatalf("output arrival after advance = %v, want 275", got)
	}
	if !IsMonitoredMetric("moox_collector_dataset_output_arrival_timestamp_seconds") || !IsMonitoredMetric("moox_storage_view_output_arrival_timestamp_seconds") {
		t.Fatal("arrival metrics must be reported to Monitor")
	}
}

func TestDatasetMetricsAcceptsCanonicalStorageFrequency(t *testing.T) {
	metrics, err := NewDatasetMetrics(prometheus.NewRegistry(), "collector")
	if err != nil {
		t.Fatal(err)
	}
	key := DatasetKey{SpaceID: "crypto", DatasetID: "dataset_spot_kline_1h", Freq: "1h"}
	if err := metrics.ReplaceExpected([]DatasetExpectation{{Key: key, Interval: time.Hour}}); err != nil {
		t.Fatalf("canonical Storage frequency rejected: %v", err)
	}
	if err := metrics.ObserveRun(DatasetObservation{
		Key: key, Result: "success", Rows: 1, FinishedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("canonical Storage frequency observation rejected: %v", err)
	}
}

func TestDatasetMetricsAcceptsSecondFrequency(t *testing.T) {
	metrics, err := NewDatasetMetrics(prometheus.NewRegistry(), "monitor")
	if err != nil {
		t.Fatal(err)
	}
	key := DatasetKey{SpaceID: "crypto", DatasetID: "dataset_binance_kline_30s", Freq: "30s"}
	if err := metrics.ObserveFact(DatasetObservation{Key: key, Result: "success", FinishedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("second frequency observation rejected: %v", err)
	}
}

func TestDatasetMetricsUsesCanonicalFrequencyIdentity(t *testing.T) {
	metrics, err := NewDatasetMetrics(prometheus.NewRegistry(), "collector")
	if err != nil {
		t.Fatal(err)
	}
	canonical := DatasetKey{SpaceID: "crypto", DatasetID: "dataset_spot_kline_1h", Freq: "1h"}
	alias := DatasetKey{SpaceID: "crypto", DatasetID: "dataset_spot_kline_1h", Freq: "1H"}
	if err := metrics.ReplaceExpected([]DatasetExpectation{{Key: canonical, Interval: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	if err := metrics.ObserveRun(DatasetObservation{
		Key: alias, Result: "success", Rows: 1, FinishedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("an alias observation did not match the canonical inventory: %v", err)
	}
}

func TestDatasetMetricsRejectsUnknownDatasetAndLabels(t *testing.T) {
	metrics, err := NewDatasetMetrics(prometheus.NewRegistry(), "collector")
	if err != nil {
		t.Fatal(err)
	}
	key := DatasetKey{SpaceID: "crypto", DatasetID: "market_kline", Freq: "1m"}
	if err := metrics.ReplaceExpected([]DatasetExpectation{{Key: key, Interval: time.Minute}}); err != nil {
		t.Fatal(err)
	}
	if err := metrics.ObserveRun(DatasetObservation{
		Key:    DatasetKey{SpaceID: "crypto", DatasetID: "other", Freq: "1m"},
		Result: "success", FinishedAt: time.Now(),
	}); err == nil {
		t.Fatal("unknown dataset was accepted")
	}
	if err := metrics.ObserveRun(DatasetObservation{Key: key, Result: "credential leaked", FinishedAt: time.Now()}); err == nil {
		t.Fatal("unbounded result label was accepted")
	}
}

func TestDatasetMetricsObserveFactDoesNotDeclareEnabledInventory(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewDatasetMetrics(registry, "storage")
	if err != nil {
		t.Fatal(err)
	}
	key := DatasetKey{SpaceID: "crypto", DatasetID: "market_kline", Freq: "1m"}
	if err := metrics.ObserveFact(DatasetObservation{
		Key: key, Result: "success", Rows: 1, FinishedAt: time.Now().UTC(),
		OutputWatermark: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() == "moox_storage_dataset_enabled" {
			t.Fatal("fact-only Storage observation declared an enabled Dataset")
		}
	}
	requireMetricFamily(t, families, "moox_storage_dataset_output_watermark_timestamp_seconds")
}

func TestDatasetMetricsAcceptsEmptyWithoutExpandingModuleResults(t *testing.T) {
	metrics, err := NewDatasetMetrics(prometheus.NewRegistry(), "collector")
	if err != nil {
		t.Fatal(err)
	}
	key := DatasetKey{SpaceID: "crypto", DatasetID: "market_kline", Freq: "1m"}
	if err := metrics.ReplaceExpected([]DatasetExpectation{{Key: key, Interval: time.Minute}}); err != nil {
		t.Fatal(err)
	}
	finishedAt := time.Now().UTC().Truncate(time.Second)
	if err := metrics.ObserveRun(DatasetObservation{Key: key, Result: "empty", FinishedAt: finishedAt}); err != nil {
		t.Fatalf("empty dataset result rejected: %v", err)
	}
	if got := testutil.ToFloat64(metrics.lastSuccess.WithLabelValues("crypto", "market_kline", "1m")); got != float64(finishedAt.Unix()) {
		t.Fatalf("empty last success = %v, want %d", got, finishedAt.Unix())
	}
	module, err := NewModuleMetrics(prometheus.NewRegistry(), "collector", []string{"market-data"})
	if err != nil {
		t.Fatal(err)
	}
	if err := module.ObserveRun("collect", "empty", "market-data", time.Now()); err == nil {
		t.Fatal("module metrics accepted dataset-only empty result")
	}
}

func TestDatasetMetricsPublishesDisplayNames(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewDatasetMetrics(registry, "strategy")
	if err != nil {
		t.Fatal(err)
	}
	if !IsMonitoredMetric("moox_strategy_dataset_info") {
		t.Fatal("名称序列应该上报给 Monitor")
	}
	named := DatasetKey{SpaceID: "crypto", DatasetID: "strategy_a", Freq: "1m"}
	unnamed := DatasetKey{SpaceID: "crypto", DatasetID: "strategy_b", Freq: "1m"}
	if err := metrics.ReplaceExpected([]DatasetExpectation{
		{Key: named, Interval: time.Minute, Name: "  动量轮动（26d51f7e）  "},
		{Key: unnamed, Interval: time.Minute},
	}); err != nil {
		t.Fatal(err)
	}
	if got := testutil.CollectAndCount(metrics.info); got != 1 {
		t.Fatalf("名称序列 %d 条，只有带名称的数据集应该有", got)
	}
	if got := testutil.ToFloat64(metrics.info.WithLabelValues("crypto", "strategy_a", "1m", "动量轮动（26d51f7e）")); got != 1 {
		t.Fatalf("名称序列取值 %v，应为 1", got)
	}

	// 改名后旧名称序列消失；移出清单的数据集名称序列也消失。
	if err := metrics.ReplaceExpected([]DatasetExpectation{{Key: named, Interval: time.Minute, Name: "均值回归（26d51f7e）"}}); err != nil {
		t.Fatal(err)
	}
	if got := testutil.CollectAndCount(metrics.info); got != 1 {
		t.Fatalf("改名后名称序列 %d 条，旧名称应已删除", got)
	}
	if got := testutil.ToFloat64(metrics.info.WithLabelValues("crypto", "strategy_a", "1m", "均值回归（26d51f7e）")); got != 1 {
		t.Fatalf("新名称序列取值 %v，应为 1", got)
	}
	if err := metrics.ReplaceExpected(nil); err != nil {
		t.Fatal(err)
	}
	if got := testutil.CollectAndCount(metrics.info); got != 0 {
		t.Fatalf("清单清空后名称序列 %d 条，应全部删除", got)
	}
}

func TestDatasetDisplayNameTruncatesOnRuneBoundary(t *testing.T) {
	long := strings.Repeat("策", MaxDatasetNameBytes)
	got := datasetDisplayName(long)
	if len(got) > MaxDatasetNameBytes || !utf8.ValidString(got) || !strings.HasSuffix(got, "…") {
		t.Fatalf("截断结果不合法：%d 字节 %q", len(got), got)
	}
	if got := datasetDisplayName("坏\xff名"); got != "坏名" {
		t.Fatalf("非法 UTF-8 应被去掉，得到 %q", got)
	}
}
