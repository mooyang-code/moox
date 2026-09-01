package healthview

import (
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/observability"
)

func TestBuilderReturnsStableEmptySections(t *testing.T) {
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	view, err := (Builder{Now: func() time.Time { return now }}).Build(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !view.GeneratedAt.Equal(now) || view.Alerts == nil || len(view.BusinessItems) != 3 || view.ServiceItems == nil {
		t.Fatalf("overview sections are not stable: %+v", view)
	}
}

func TestMaskURLNeverReturnsFullSecret(t *testing.T) {
	const raw = "https://example.com/hooks/super-secret"
	if got := MaskURL(raw); got == raw || got != "https://...cret" {
		t.Fatalf("masked URL = %q", got)
	}
}

func TestMergeItemKeepsWorstStatusAndLatestObservation(t *testing.T) {
	items := map[string]Item{}
	mergeItem(items, "business:行情采集", Item{Name: "行情采集", Status: "healthy", CheckedAt: time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)})
	mergeItem(items, "business:行情采集", Item{Name: "行情采集", Status: "down", Reason: "Timer 不可用", CheckedAt: time.Date(2026, 8, 23, 12, 1, 0, 0, time.UTC)})
	got := items["business:行情采集"]
	if got.Status != "down" || got.Reason != "Timer 不可用" || got.OmittedInstanceCount != 0 || !got.CheckedAt.Equal(time.Date(2026, 8, 23, 12, 1, 0, 0, time.UTC)) {
		t.Fatalf("merged item = %+v", got)
	}
}

func TestMergeItemReplacesEmptyCatalogUnknownWithHealthyFact(t *testing.T) {
	items := map[string]Item{"business:行情采集": {Name: "行情采集", Status: "unknown"}}
	mergeItem(items, "business:行情采集", Item{Name: "行情采集", Status: "healthy", Instances: []Instance{{Name: "binance_spot_kline_1m"}}})
	if got := items["business:行情采集"]; got.Status != "healthy" || len(got.Instances) != 1 {
		t.Fatalf("catalog item was not replaced by healthy fact: %+v", got)
	}
}

func TestMergeItemCountsOnlyTruncatedInstances(t *testing.T) {
	items := map[string]Item{}
	mergeItem(items, "business:行情采集", Item{Name: "行情采集", Status: "healthy", Instances: []Instance{{Name: "a"}, {Name: "b"}}})
	if got := items["business:行情采集"].OmittedInstanceCount; got != 0 {
		t.Fatalf("visible instances were counted as omitted: %d", got)
	}
	items = map[string]Item{}
	for i := 0; i < maxInstancesPerItem+1; i++ {
		mergeItem(items, "business:行情采集", Item{Name: "行情采集", Status: "healthy", Instances: []Instance{{Name: string(rune(i))}}})
	}
	got := items["business:行情采集"]
	if len(got.Instances) != maxInstancesPerItem || got.OmittedInstanceCount != 1 {
		t.Fatalf("truncated instances = %d, omitted = %d", len(got.Instances), got.OmittedInstanceCount)
	}
}

func TestBusinessCatalogRejectsTechnicalFacts(t *testing.T) {
	if isBusinessName("核心服务") {
		t.Fatal("technical service facts must not create a fourth business category")
	}
}

func TestServiceNameUsesChineseBusinessLabels(t *testing.T) {
	for raw, want := range map[string]string{"moox-collector": "行情采集", "storage-view": "数据存储", "moox-factor": "因子计算", "trade": "交易管理"} {
		got, _ := serviceName(raw)
		if got != want {
			t.Fatalf("serviceName(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestHealthViewKeepsHostMonitoringOutOfCoreServices(t *testing.T) {
	if name, _ := serviceName("moox-host-agent"); name != "主机监控" {
		t.Fatalf("host agent label = %q", name)
	}
	// The Builder skips this service label; host details remain available from the host workbench.
	if name, _ := serviceName("moox-factor"); name == "主机监控" {
		t.Fatal("factor must not be classified as host monitoring")
	}
}

func TestHealthViewExcludesHostMonitoringDatasetsAndAlerts(t *testing.T) {
	for _, item := range []observability.DatasetFrequencyStatus{
		{DatasetID: "host_resource_v1"},
		{DatasetID: "host_fs_view"},
		{DatasetID: "HOST_NET_VIEW"},
	} {
		if !isHostMonitoringDataset(item) {
			t.Fatalf("host dataset %q was not filtered", item.DatasetID)
		}
	}
	if isHostMonitoringDataset(observability.DatasetFrequencyStatus{DatasetID: "moox_service_metrics_view"}) {
		t.Fatal("service metrics view must not be classified as host monitoring")
	}
	for _, checkID := range []string{
		"host:AB12:cpu",
		"dataset:storage:host_resource_v1:1m",
		"dataset:storage_view:host_fs_view:1m",
	} {
		if !isHostMonitoringCheck(checkID) {
			t.Fatalf("host check %q was not filtered", checkID)
		}
	}
	if isHostMonitoringCheck("dataset:storage:binance_spot_kline_1m:1m") {
		t.Fatal("market dataset check was incorrectly classified as host monitoring")
	}
}

func TestCollectorDatasetUsesStorageAsAuthoritativeHealthFact(t *testing.T) {
	items := []observability.DatasetFrequencyStatus{
		{Producer: "collector", SpaceID: "crypto", DatasetID: "binance_spot_kline_1m", Freq: "1m"},
		{Producer: "storage", SpaceID: "crypto", DatasetID: "binance_spot_kline_1m", Freq: "1m"},
		{Producer: "collector", SpaceID: "crypto", DatasetID: "new_dataset", Freq: "1m"},
	}
	scopes := storageDatasetScopes(items)
	if !collectorCoveredByStorage(items[0], scopes) {
		t.Fatal("collector fact with a Primary Storage fact must be covered")
	}
	if collectorCoveredByStorage(items[2], scopes) {
		t.Fatal("collector fact without a Primary Storage fact must remain visible")
	}
	if collectorCoveredByStorage(items[1], scopes) {
		t.Fatal("non-collector fact must not be filtered")
	}
}

func TestCollectorDatasetCoverageNormalizesFrequency(t *testing.T) {
	items := []observability.DatasetFrequencyStatus{
		{Producer: "storage", SpaceID: "crypto", DatasetID: "bars", Freq: "1H"},
	}
	if !collectorCoveredByStorage(
		observability.DatasetFrequencyStatus{Producer: "collector", SpaceID: "crypto", DatasetID: "bars", Freq: "1h"},
		storageDatasetScopes(items),
	) {
		t.Fatal("frequency matching must be case-insensitive")
	}
}

func TestChineseReasonTranslatesTechnicalResults(t *testing.T) {
	for raw, want := range map[string]string{
		"reporter fresh":                     "监控上报正常",
		"reporter fresh; health check ok":    "监控上报正常；健康检查正常",
		"health check failed":                "健康检查失败",
		"balance difference 0.2 exceeds 0.1": "账户余额差异超过阈值（当前值 0.2，阈值 0.1）",
		"unexpected timeout":                 "监控检查失败，请查看日志详情",
	} {
		if got := ChineseReason(raw); got != want {
			t.Fatalf("ChineseReason(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestBusinessNameCollapsesStorageFactsIntoMarketHealth(t *testing.T) {
	name, description := businessName("storage-view:binance_spot_kline_1m")
	if name != "行情采集" || description == "" {
		t.Fatalf("businessName() = %q, %q", name, description)
	}
}

func TestBusinessNameCollapsesMarketCanaryIntoMarketHealth(t *testing.T) {
	name, description := businessName("observability:canary")
	if name != "行情采集" || description == "" {
		t.Fatalf("businessName() = %q, %q", name, description)
	}
}

func TestAlertTitleUsesHumanReadableHostAndServiceNames(t *testing.T) {
	if got := alertTitle("host:AB12:filesystem_usage"); got != "主机 AB12 · 磁盘占用率" {
		t.Fatalf("host alert title = %q", got)
	}
	if got := alertTitle("sysdeploy:control:storage-view"); got != "数据存储" {
		t.Fatalf("service alert title = %q", got)
	}
	if got := alertTitle("dataset:factor:binance_spot_kline_1m_factor:1m"); got != "因子计算任务 · binance_spot_kline_1m_factor / 1m" {
		t.Fatalf("factor alert title = %q", got)
	}
	if got := alertTitle("dataset:storage:binance_spot_kline_1m_factor:1m"); got != "因子结果数据 · binance_spot_kline_1m_factor / 1m" {
		t.Fatalf("storage alert title = %q", got)
	}
	if got := alertTitle("dataset:storage_view:binance_spot_kline_1m_view:1m"); got != "行情结果视图 · binance_spot_kline_1m_view / 1m" {
		t.Fatalf("market view alert title = %q", got)
	}
	if got := alertTitle("sysdeploy:control:moox_factor"); got != "因子计算服务" {
		t.Fatalf("factor service alert title = %q", got)
	}
}

func TestDatasetAlertReasonIncludesFreshnessFacts(t *testing.T) {
	now := time.Date(2026, 8, 23, 10, 38, 30, 0, time.UTC)
	item := observability.DatasetFrequencyStatus{
		DatasetID:         "binance_spot_kline_1m_factor",
		Freq:              "1m",
		Reason:            "run stale",
		LastRunAt:         time.Date(2026, 8, 23, 10, 29, 1, 0, time.UTC),
		LastSuccessAt:     time.Date(2026, 8, 23, 10, 29, 1, 0, time.UTC),
		OutputWatermarkAt: time.Date(2026, 8, 23, 10, 27, 0, 0, time.UTC),
		LagSeconds:        690,
	}
	got := datasetAlertReason(item, now)
	for _, want := range []string{"binance_spot_kline_1m_factor（1m）", "最近运行 2026-08-23 10:29:01 UTC", "最近成功 2026-08-23 10:29:01 UTC", "最新输出 2026-08-23 10:27:00 UTC", "当前落后 11 分 30 秒"} {
		if !strings.Contains(got, want) {
			t.Fatalf("reason %q does not contain %q", got, want)
		}
	}
}

func TestFindDatasetAlertFactMatchesFrequencyAndProducer(t *testing.T) {
	datasets := []observability.DatasetFrequencyStatus{{Producer: "factor", DatasetID: "bars", Freq: "1m", Reason: "run stale"}}
	got, ok := findDatasetAlertFact("dataset:factor:bars:1m", datasets)
	if !ok || got.DatasetID != "bars" {
		t.Fatalf("fact = %+v, ok = %v", got, ok)
	}
	if _, ok := findDatasetAlertFact("dataset:storage:bars:1m", datasets); ok {
		t.Fatal("fact from another producer must not match")
	}
}

func TestCheckResultReasonExplainsHealthTimeout(t *testing.T) {
	if got := checkResultReason(`Get "http://127.0.0.1:11414/readyz": context deadline exceeded`); got != "健康检查超时：服务未能在规定时间内返回就绪状态" {
		t.Fatalf("timeout reason = %q", got)
	}
	if got := checkResultReason("Timer 不可用"); got != "Timer 不可用" {
		t.Fatalf("business reason = %q", got)
	}
}

func TestHealthRankPutsFailuresFirst(t *testing.T) {
	items := []Item{{Name: "健康", Status: "healthy"}, {Name: "异常", Status: "down"}}
	sort.Slice(items, func(i, j int) bool { return healthRank(items[i].Status) > healthRank(items[j].Status) })
	if items[0].Name != "异常" {
		t.Fatalf("items = %+v", items)
	}
}
