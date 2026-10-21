package observability

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestDatasetStatusDistinguishesMissingStaleAndEmpty(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	key := datasetKey{producer: "collector", spaceID: "mooxsys", datasetID: "market_1m", freq: "1m"}
	policy := testRealtimePolicy()
	inventory := float64(now.Unix())

	missing := datasetStatus(now, key, datasetValues{interval: 60, inventory: inventory}, policy, cryptoClock)
	if missing.Status != "unknown" || missing.Reason != "尚未上报" {
		t.Fatalf("missing = %+v", missing)
	}
	stale := datasetStatus(now, key, datasetValues{
		interval: 60, lastRun: float64(now.Add(-10 * time.Minute).Unix()),
		lastSuccess: float64(now.Add(-10 * time.Minute).Unix()), output: float64(now.Add(-10 * time.Minute).Unix()),
		inventory: inventory,
	}, policy, cryptoClock)
	if stale.Status != "stale" || stale.Reason != "run stale" || stale.WaitingBars != 9 {
		t.Fatalf("stale = %+v", stale)
	}
	empty := datasetStatus(now, key, datasetValues{
		interval: 60, inventory: inventory, lastRun: float64(now.Unix()), lastSuccess: float64(now.Unix()),
	}, policy, cryptoClock)
	if empty.Status != "healthy" || empty.Reason != "正常但空结果" {
		t.Fatalf("empty = %+v", empty)
	}
	producerStale := datasetStatus(now, key, datasetValues{
		reporterStale: true, inventory: inventory, lastRun: float64(now.Unix()), lastSuccess: float64(now.Unix()),
	}, policy, cryptoClock)
	if producerStale.Status != "stale" || producerStale.Reason != "producer stale" {
		t.Fatalf("producer stale = %+v", producerStale)
	}
}

func TestDatasetTolerancesWhenIntervalIsShorterThanFrequency(t *testing.T) {
	for _, freq := range []string{"1h", "1H"} {
		key := datasetKey{spaceID: "crypto", datasetID: "market_kline", freq: freq}
		runLag, successLag, lateness := datasetTolerances(key, 60, testRealtimePolicy())
		if runLag != 2*time.Minute+30*time.Second {
			t.Fatalf("%s run lag = %s", freq, runLag)
		}
		if successLag != 3*time.Minute+30*time.Second {
			t.Fatalf("%s success lag = %s", freq, successLag)
		}
		if lateness != time.Hour {
			t.Fatalf("%s lateness = %s", freq, lateness)
		}
	}
}

// 允许迟到只按周期分级，不随期望间隔放大：应收的最新一根已按交易日历确定，周末、长假不会被算成迟到。日线及以上为 1 个周期。
func TestDatasetLatenessDoesNotScaleWithExpectedInterval(t *testing.T) {
	key := datasetKey{spaceID: "stockcn", datasetID: "strategy_signal", freq: "1d"}
	_, _, lateness := datasetTolerances(key, (40 * time.Hour).Seconds(), testRealtimePolicy())
	if lateness != 24*time.Hour {
		t.Fatalf("lateness = %s", lateness)
	}
}

func TestDatasetTolerancesMatchFrequencyAliasesInOverrides(t *testing.T) {
	policy := testRealtimePolicy()
	policy.Overrides = []report.RealtimeTimeSeriesOverride{{
		SpaceID: "crypto", DatasetID: "market_kline", Freq: "1h", Lateness: 90 * time.Minute,
	}}

	_, _, watermarkLag := datasetTolerances(
		datasetKey{spaceID: "crypto", datasetID: "market_kline", freq: "1h"},
		60,
		policy,
	)

	if watermarkLag != 90*time.Minute {
		t.Fatalf("watermark lag = %s", watermarkLag)
	}
}

func TestDatasetStatusRejectsStaleInventory(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	got := datasetStatus(now, datasetKey{producer: "collector", spaceID: "crypto", datasetID: "market_kline", freq: "1m"}, datasetValues{
		interval: 60, inventory: float64(now.Add(-11 * time.Minute).Unix()),
		lastRun: float64(now.Unix()), lastSuccess: float64(now.Unix()),
	}, testRealtimePolicy(), cryptoClock)
	if got.Status != "unknown" || got.Reason != "inventory_stale" {
		t.Fatalf("status = %+v", got)
	}
}

var cryptoClock = spaceClock{clock: monmetrics.ContinuousClock()}

func testRealtimePolicy() report.RealtimeTimeSeriesPolicy {
	return report.RealtimeTimeSeriesPolicy{Defaults: report.RealtimeTimeSeriesDefaults{
		RunMissedIntervals: 2, SuccessMissedIntervals: 3, MinuteLateness: 10 * time.Minute, HourLateness: time.Hour,
	}}
}

func TestOverviewSortsAbnormalRowsFirst(t *testing.T) {
	now := time.Now().UTC()
	overview := Overview{
		Services: []ServiceStatus{
			{ServiceName: "healthy-service", Status: "healthy", LastSeenAt: now},
			{ServiceName: "failed-service", Status: "stale", LastSeenAt: now.Add(-time.Minute)},
		},
		Datasets: []DatasetFrequencyStatus{
			{DatasetID: "ok", Status: "healthy"},
			{DatasetID: "late", Status: "stale"},
		},
	}
	sortOverview(&overview)
	if overview.Services[0].Status != "stale" || overview.Datasets[0].Status != "stale" {
		t.Fatalf("abnormal rows were not first: %+v", overview)
	}
}

func TestMergeServiceHealthKeepsProbeAndReporterTimesSeparate(t *testing.T) {
	reporterAt := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	checkedAt := reporterAt.Add(2 * time.Minute)
	service := ServiceStatus{Status: "healthy", ReporterStatus: "healthy", LastSeenAt: reporterAt}
	got := mergeServiceHealth(service, &domain.CheckResult{Success: true, Status: domain.CheckStatusOK, CheckedAt: checkedAt})
	if !got.LastSeenAt.Equal(reporterAt) || !got.ProbeCheckedAt.Equal(checkedAt) {
		t.Fatalf("last report = %s, want %s", got.LastSeenAt, reporterAt)
	}
}

func TestDatasetStatusUsesMetricObservationAsLastReport(t *testing.T) {
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	reportedAt := now.Add(-30 * time.Second)
	got := datasetStatus(now, datasetKey{producer: "collector", spaceID: "crypto", datasetID: "bars", freq: "1m"}, datasetValues{
		interval: 60, lastRun: float64(now.Unix()), lastSuccess: float64(now.Unix()), reportedAt: reportedAt,
	}, testRealtimePolicy(), cryptoClock)
	if !got.LastReportedAt.Equal(reportedAt) {
		t.Fatalf("last report = %s, want %s", got.LastReportedAt, reportedAt)
	}
}

func TestBuilderDeduplicatesServiceBootHistory(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query, repositories := openOverviewState(t, func(db *gorm.DB) {
		for _, service := range []monmetrics.MetricService{
			{
				ServiceName: "collector", InstanceID: "collector@node-a", BootID: "boot-old",
				NodeID: "node-a", LastSeenAt: now.Add(-10 * time.Minute),
			},
			{
				ServiceName: "collector", InstanceID: "collector@node-a", BootID: "boot-new",
				NodeID: "node-a", LastSeenAt: now,
			},
		} {
			if err := db.Create(&service).Error; err != nil {
				t.Fatal(err)
			}
		}
	})
	seedServiceTopology(t, repositories, "node-a", "collector", true)
	got, err := (Builder{
		Metrics: query, Checks: repositories.Checks, Topology: repositories.Topology, Results: repositories.Results,
	}).Build(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Services) != 1 || got.Services[0].ReporterStatus != "healthy" ||
		!got.Services[0].LastSeenAt.Equal(now) {
		t.Fatalf("services = %+v", got.Services)
	}
}

func TestBuilderReportsTimerCoordinationHealth(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query, _ := openOverviewState(t, func(db *gorm.DB) {
		labels := `{"space_id":"crypto","dataset_id":"bars","frequency":"1m"}`
		seedOverviewMetricForInstance(t, db, "timer-required", "collector", "collector@control", "moox_collector_market_fetch_assignment_required", labels, 1, now)
		seedOverviewMetricForInstance(t, db, "timer-active", "collector", "collector@control", "moox_collector_market_fetch_assignment_active", labels, 1, now)
		seedOverviewMetricForInstance(t, db, "timer-success", "collector", "collector@control", "moox_collector_market_fetch_assignment_last_success_timestamp_seconds", `{"space_id":"crypto"}`, float64(now.Unix()), now)
		seedOverviewMetricForInstance(t, db, "timer-trigger", "collector", "collector@control", "moox_collector_market_fetch_timer_available", `{"space_id":"crypto","node_id":"timer-1","enabled":"true"}`, 1, now)
	})
	got, err := (Builder{Metrics: query, Now: func() time.Time { return now }}).Build(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.BusinessChecks) != 1 || got.BusinessChecks[0].Kind != "market_fetch" || got.BusinessChecks[0].Status != "healthy" || got.BusinessChecks[0].SpaceID != "crypto" {
		t.Fatalf("business checks = %+v", got.BusinessChecks)
	}
}

func TestBuilderUsesConfiguredTimerCoordinationThreshold(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	lastSuccess := now.Add(-2 * time.Minute)
	query, _ := openOverviewState(t, func(db *gorm.DB) {
		labels := `{"space_id":"stockcn","dataset_id":"dataset_stockcn_equity_kline_1m","frequency":"1m"}`
		seedOverviewMetricForInstance(t, db, "timer-required-stock", "collector", "collector@control", "moox_collector_market_fetch_assignment_required", labels, 1, now)
		seedOverviewMetricForInstance(t, db, "timer-active-stock", "collector", "collector@control", "moox_collector_market_fetch_assignment_active", labels, 1, now)
		seedOverviewMetricForInstance(t, db, "timer-success-stock", "collector", "collector@control", "moox_collector_market_fetch_assignment_last_success_timestamp_seconds", `{"space_id":"stockcn"}`, float64(lastSuccess.Unix()), now)
	})

	got, err := (Builder{
		Metrics: query, Now: func() time.Time { return now },
		MarketFetchThresholds: MarketFetchThresholds{CoordinationStaleAfter: time.Minute, PendingGrace: 30 * time.Second},
	}).Build(t.Context(), "stockcn")

	require.NoError(t, err)
	require.Len(t, got.BusinessChecks, 1)
	require.Equal(t, "down", got.BusinessChecks[0].Status)
	require.Contains(t, got.BusinessChecks[0].Reason, "超过允许时间")
}

func TestBuilderIgnoresDisabledTimerNodes(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query, _ := openOverviewState(t, func(db *gorm.DB) {
		labels := `{"space_id":"crypto","dataset_id":"bars","frequency":"1m"}`
		seedOverviewMetricForInstance(t, db, "timer-required", "collector", "collector@control", "moox_collector_market_fetch_assignment_required", labels, 1, now)
		seedOverviewMetricForInstance(t, db, "timer-active", "collector", "collector@control", "moox_collector_market_fetch_assignment_active", labels, 1, now)
		seedOverviewMetricForInstance(t, db, "timer-success", "collector", "collector@control", "moox_collector_market_fetch_assignment_last_success_timestamp_seconds", `{"space_id":"crypto"}`, float64(now.Unix()), now)
		seedOverviewMetricForInstance(t, db, "disabled-trigger", "collector", "collector@control", "moox_collector_market_fetch_timer_available", `{"space_id":"crypto","node_id":"timer-disabled","enabled":"false"}`, 0, now)
	})
	got, err := (Builder{Metrics: query, Now: func() time.Time { return now }}).Build(t.Context(), "")
	require.NoError(t, err)
	require.Len(t, got.BusinessChecks, 1)
	require.Equal(t, "healthy", got.BusinessChecks[0].Status)
}

func TestBuilderDoesNotAlertDuringExpectedAsyncTimerBatch(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query, _ := openOverviewState(t, func(db *gorm.DB) {
		labels := `{"space_id":"crypto","dataset_id":"bars","frequency":"1m"}`
		lastSuccess := now.Add(-10 * time.Minute)
		// Keep the metric series fresh while making the coordination value old;
		// FindSeriesAt marks an unreported series stale independently of its value.
		seedOverviewMetricForInstance(t, db, "timer-required", "collector", "collector@control", "moox_collector_market_fetch_assignment_required", labels, 1, now)
		seedOverviewMetricForInstance(t, db, "timer-active", "collector", "collector@control", "moox_collector_market_fetch_assignment_active", labels, 1, now)
		seedOverviewMetricForInstance(t, db, "timer-success", "collector", "collector@control", "moox_collector_market_fetch_assignment_last_success_timestamp_seconds", `{"space_id":"crypto"}`, float64(lastSuccess.Unix()), now)
		seedOverviewMetricForInstance(t, db, "timer-trigger", "collector", "collector@control", "moox_collector_market_fetch_timer_available", `{"space_id":"crypto","node_id":"timer-1","enabled":"true"}`, 1, now)
	})
	got, err := (Builder{Metrics: query, Now: func() time.Time { return now }}).Build(t.Context(), "")
	require.NoError(t, err)
	require.Len(t, got.BusinessChecks, 1)
	require.Equal(t, "healthy", got.BusinessChecks[0].Status)
}

func TestBuilderSuppressesShortPendingTimerBatch(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query, _ := openOverviewState(t, func(db *gorm.DB) {
		labels := `{"space_id":"crypto","dataset_id":"bars","frequency":"1m"}`
		lastSuccess := now.Add(-20 * time.Minute)
		pendingSince := now.Add(-2 * time.Minute)
		seedOverviewMetricForInstance(t, db, "pending-required", "collector", "collector@control", "moox_collector_market_fetch_assignment_required", labels, 1, now)
		seedOverviewMetricForInstance(t, db, "pending-active", "collector", "collector@control", "moox_collector_market_fetch_assignment_active", labels, 1, now)
		seedOverviewMetricForInstance(t, db, "pending-success", "collector", "collector@control", "moox_collector_market_fetch_assignment_last_success_timestamp_seconds", `{"space_id":"crypto"}`, float64(lastSuccess.Unix()), now)
		seedOverviewMetricForInstance(t, db, "pending-health", "collector", "collector@control", "moox_collector_market_fetch_coordination_healthy", `{"space_id":"crypto"}`, 0, now)
		seedOverviewMetricForInstance(t, db, "pending-state", "collector", "collector@control", "moox_collector_market_fetch_coordination_pending", `{"space_id":"crypto"}`, 1, now)
		seedOverviewMetricForInstance(t, db, "pending-since", "collector", "collector@control", "moox_collector_market_fetch_coordination_pending_since_timestamp_seconds", `{"space_id":"crypto"}`, float64(pendingSince.Unix()), now)
		seedOverviewMetricForInstance(t, db, "pending-trigger", "collector", "collector@control", "moox_collector_market_fetch_timer_available", `{"space_id":"crypto","node_id":"timer-1","enabled":"true"}`, 1, now)
	})
	got, err := (Builder{Metrics: query, Now: func() time.Time { return now }}).Build(t.Context(), "")
	require.NoError(t, err)
	require.Len(t, got.BusinessChecks, 1)
	require.Equal(t, "healthy", got.BusinessChecks[0].Status)
	require.Contains(t, got.BusinessChecks[0].Reason, "协调进行中")
}

// seedLeaseHeldFailure 构造「协调失败，原因是发布租约被占用」的指标：failureAge 为 0 表示不上报失败起始时间。
func seedLeaseHeldFailure(t *testing.T, db *gorm.DB, prefix string, now time.Time, failureAge time.Duration) {
	t.Helper()
	labels := `{"space_id":"crypto","dataset_id":"bars","frequency":"1m"}`
	seedOverviewMetricForInstance(t, db, prefix+"-required", "collector", "collector@control", "moox_collector_market_fetch_assignment_required", labels, 34, now)
	seedOverviewMetricForInstance(t, db, prefix+"-active", "collector", "collector@control", "moox_collector_market_fetch_assignment_active", labels, 34, now)
	seedOverviewMetricForInstance(t, db, prefix+"-health", "collector", "collector@control", "moox_collector_market_fetch_coordination_healthy", `{"space_id":"crypto"}`, 0, now)
	seedOverviewMetricForInstance(t, db, prefix+"-reason", "collector", "collector@control", "moox_collector_market_fetch_coordination_failure", `{"space_id":"crypto","reason":"publish_lease_held"}`, 1, now)
	if failureAge > 0 {
		seedOverviewMetricForInstance(t, db, prefix+"-since", "collector", "collector@control", "moox_collector_market_fetch_coordination_failure_since_timestamp_seconds", `{"space_id":"crypto"}`, float64(now.Add(-failureAge).Unix()), now)
	}
}

func TestBuilderTreatsRecentPublishLeaseHeldAsCoordinationInProgress(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query, _ := openOverviewState(t, func(db *gorm.DB) { seedLeaseHeldFailure(t, db, "held-recent", now, 6*time.Minute) })
	got, err := (Builder{Metrics: query, Now: func() time.Time { return now }}).Build(t.Context(), "")
	require.NoError(t, err)
	require.Len(t, got.BusinessChecks, 1)
	require.Equal(t, "healthy", got.BusinessChecks[0].Status)
	require.Contains(t, got.BusinessChecks[0].Reason, "发布租约被占用")
	require.NotContains(t, got.BusinessChecks[0].Reason, "CloudNode 服务调用失败")
}

func TestBuilderAlertsWhenPublishLeaseStaysHeldPastTheGrace(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query, _ := openOverviewState(t, func(db *gorm.DB) {
		seedLeaseHeldFailure(t, db, "held-old", now, timerCoordinationLeaseGrace+time.Minute)
	})
	got, err := (Builder{Metrics: query, Now: func() time.Time { return now }}).Build(t.Context(), "")
	require.NoError(t, err)
	require.Len(t, got.BusinessChecks, 1)
	require.Equal(t, "down", got.BusinessChecks[0].Status)
	require.Contains(t, got.BusinessChecks[0].Reason, "发布租约长时间被占用")
	require.Contains(t, got.BusinessChecks[0].Reason, "批次作业是否卡住")
}

func TestBuilderDoesNotGivePublishLeaseGraceWithoutAFailureStartTime(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query, _ := openOverviewState(t, func(db *gorm.DB) { seedLeaseHeldFailure(t, db, "held-unknown", now, 0) })
	got, err := (Builder{Metrics: query, Now: func() time.Time { return now }}).Build(t.Context(), "")
	require.NoError(t, err)
	require.Len(t, got.BusinessChecks, 1)
	require.Equal(t, "down", got.BusinessChecks[0].Status, "无法证明还在宽限期内时保持保守")
	require.Contains(t, got.BusinessChecks[0].Reason, "发布租约长时间被占用")
}

func TestBuilderLeaseGraceIsConfigurable(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query, _ := openOverviewState(t, func(db *gorm.DB) { seedLeaseHeldFailure(t, db, "held-cfg", now, 3*time.Minute) })
	build := func(grace time.Duration) string {
		got, err := (Builder{Metrics: query, Now: func() time.Time { return now }, MarketFetchThresholds: MarketFetchThresholds{LeaseGrace: grace}}).Build(t.Context(), "")
		require.NoError(t, err)
		require.Len(t, got.BusinessChecks, 1)
		return got.BusinessChecks[0].Status
	}
	require.Equal(t, "healthy", build(5*time.Minute))
	require.Equal(t, "down", build(2*time.Minute))
}

func TestBuilderLeaseGraceDoesNotHideAnotherInstancesRealFailure(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query, _ := openOverviewState(t, func(db *gorm.DB) {
		seedLeaseHeldFailure(t, db, "mixed", now, time.Minute)
		// 另一个 Collector 实例同时报告真实故障：聚合后原因集合包含两者，租约宽限不能把它盖住。
		seedOverviewMetricForInstance(t, db, "mixed-other", "collector", "collector@standby", "moox_collector_market_fetch_coordination_failure", `{"space_id":"crypto","reason":"cloudnode"}`, 1, now)
	})
	got, err := (Builder{Metrics: query, Now: func() time.Time { return now }}).Build(t.Context(), "")
	require.NoError(t, err)
	require.Len(t, got.BusinessChecks, 1)
	require.Equal(t, "down", got.BusinessChecks[0].Status)
	require.Contains(t, got.BusinessChecks[0].Reason, "配置服务调用失败", "真实故障的文案优先")
}

func TestBuilderMixedReasonsWithoutDedicatedTextDoNotBlameTheLease(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query, _ := openOverviewState(t, func(db *gorm.DB) {
		seedLeaseHeldFailure(t, db, "mixed-capacity", now, time.Minute)
		seedOverviewMetricForInstance(t, db, "mixed-capacity-other", "collector", "collector@standby", "moox_collector_market_fetch_coordination_failure", `{"space_id":"crypto","reason":"capacity"}`, 1, now)
	})
	got, err := (Builder{Metrics: query, Now: func() time.Time { return now }}).Build(t.Context(), "")
	require.NoError(t, err)
	require.Len(t, got.BusinessChecks, 1)
	require.Equal(t, "down", got.BusinessChecks[0].Status)
	require.NotContains(t, got.BusinessChecks[0].Reason, "发布租约长时间被占用", "租约冲突才持续一分钟，不能把告警归因到租约")
	require.Contains(t, got.BusinessChecks[0].Reason, "采集计划协调失败")
}

func TestBuilderPendingGraceDoesNotOverrideAnExpiredLeaseGrace(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	seed := func(failureAge time.Duration) func(db *gorm.DB) {
		return func(db *gorm.DB) {
			seedLeaseHeldFailure(t, db, "pg", now, failureAge)
			// 开放计划刚创建：普通 pending 宽限（默认 5 分钟）还没过。
			seedOverviewMetricForInstance(t, db, "pg-state", "collector", "collector@control", "moox_collector_market_fetch_coordination_pending", `{"space_id":"crypto"}`, 1, now)
			seedOverviewMetricForInstance(t, db, "pg-pending-since", "collector", "collector@control", "moox_collector_market_fetch_coordination_pending_since_timestamp_seconds", `{"space_id":"crypto"}`, float64(now.Add(-time.Minute).Unix()), now)
		}
	}
	build := func(grace time.Duration, failureAge time.Duration) BusinessStatus {
		query, _ := openOverviewState(t, seed(failureAge))
		got, err := (Builder{Metrics: query, Now: func() time.Time { return now }, MarketFetchThresholds: MarketFetchThresholds{LeaseGrace: grace}}).Build(t.Context(), "")
		require.NoError(t, err)
		require.Len(t, got.BusinessChecks, 1)
		return got.BusinessChecks[0]
	}
	// 租约宽限 2 分钟、失败已持续 3 分钟：即使 pending 宽限还没过，也要告警。
	expired := build(2*time.Minute, 3*time.Minute)
	require.Equal(t, "down", expired.Status)
	require.Contains(t, expired.Reason, "发布租约长时间被占用")
	// 缺少失败起始时间：无法证明还在宽限期内，同样不能被 pending 宽限盖住。
	unknown := build(10*time.Minute, 0)
	require.Equal(t, "down", unknown.Status)
	// 租约宽限内：仍按协调进行中处理。
	within := build(10*time.Minute, 3*time.Minute)
	require.Equal(t, "healthy", within.Status)
}

func TestBuilderOnlyLeaseHeldGetsTheGraceNotOtherFailureReasons(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query, _ := openOverviewState(t, func(db *gorm.DB) {
		labels := `{"space_id":"crypto","dataset_id":"bars","frequency":"1m"}`
		seedOverviewMetricForInstance(t, db, "other-required", "collector", "collector@control", "moox_collector_market_fetch_assignment_required", labels, 34, now)
		seedOverviewMetricForInstance(t, db, "other-active", "collector", "collector@control", "moox_collector_market_fetch_assignment_active", labels, 34, now)
		seedOverviewMetricForInstance(t, db, "other-health", "collector", "collector@control", "moox_collector_market_fetch_coordination_healthy", `{"space_id":"crypto"}`, 0, now)
		seedOverviewMetricForInstance(t, db, "other-reason", "collector", "collector@control", "moox_collector_market_fetch_coordination_failure", `{"space_id":"crypto","reason":"cloudnode"}`, 1, now)
		// 失败刚刚开始，但原因不是「被占用」：Admin 不可达、无权限等照常告警。
		seedOverviewMetricForInstance(t, db, "other-since", "collector", "collector@control", "moox_collector_market_fetch_coordination_failure_since_timestamp_seconds", `{"space_id":"crypto"}`, float64(now.Add(-time.Minute).Unix()), now)
	})
	got, err := (Builder{Metrics: query, Now: func() time.Time { return now }}).Build(t.Context(), "")
	require.NoError(t, err)
	require.Len(t, got.BusinessChecks, 1)
	require.Equal(t, "down", got.BusinessChecks[0].Status)
	require.Contains(t, got.BusinessChecks[0].Reason, "配置服务调用失败")
}

func TestBuilderDoesNotMislabelCloudNodeFailureAsCapacityShortage(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query, _ := openOverviewState(t, func(db *gorm.DB) {
		labels := `{"space_id":"crypto","dataset_id":"bars","frequency":"1m"}`
		seedOverviewMetricForInstance(t, db, "cloudnode-required", "collector", "collector@control", "moox_collector_market_fetch_assignment_required", labels, 34, now)
		seedOverviewMetricForInstance(t, db, "cloudnode-active", "collector", "collector@control", "moox_collector_market_fetch_assignment_active", labels, 0, now)
		seedOverviewMetricForInstance(t, db, "cloudnode-health", "collector", "collector@control", "moox_collector_market_fetch_coordination_healthy", `{"space_id":"crypto"}`, 0, now)
		seedOverviewMetricForInstance(t, db, "cloudnode-reason", "collector", "collector@control", "moox_collector_market_fetch_coordination_failure", `{"space_id":"crypto","reason":"cloudnode"}`, 1, now)
		seedOverviewMetricForInstance(t, db, "cloudnode-capacity-total", "collector", "collector@control", "moox_collector_market_fetch_timer_capacity_total", `{"space_id":"crypto"}`, 60, now)
		seedOverviewMetricForInstance(t, db, "cloudnode-capacity-required", "collector", "collector@control", "moox_collector_market_fetch_timer_capacity_required", `{"space_id":"crypto"}`, 34, now)
	})
	got, err := (Builder{Metrics: query, Now: func() time.Time { return now }}).Build(t.Context(), "")
	require.NoError(t, err)
	require.Len(t, got.BusinessChecks, 1)
	require.Equal(t, "down", got.BusinessChecks[0].Status)
	require.Contains(t, got.BusinessChecks[0].Reason, "配置服务调用失败")
	require.NotContains(t, got.BusinessChecks[0].Reason, "节点不足")
}

func TestBuilderAlertsWhenTimerBatchPendingTooLong(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query, _ := openOverviewState(t, func(db *gorm.DB) {
		labels := `{"space_id":"crypto","dataset_id":"bars","frequency":"1m"}`
		pendingSince := now.Add(-(timerCoordinationPendingGrace + time.Minute))
		seedOverviewMetricForInstance(t, db, "slow-required", "collector", "collector@control", "moox_collector_market_fetch_assignment_required", labels, 1, now)
		seedOverviewMetricForInstance(t, db, "slow-active", "collector", "collector@control", "moox_collector_market_fetch_assignment_active", labels, 1, now)
		seedOverviewMetricForInstance(t, db, "slow-health", "collector", "collector@control", "moox_collector_market_fetch_coordination_healthy", `{"space_id":"crypto"}`, 0, now)
		seedOverviewMetricForInstance(t, db, "slow-state", "collector", "collector@control", "moox_collector_market_fetch_coordination_pending", `{"space_id":"crypto"}`, 1, now)
		seedOverviewMetricForInstance(t, db, "slow-since", "collector", "collector@control", "moox_collector_market_fetch_coordination_pending_since_timestamp_seconds", `{"space_id":"crypto"}`, float64(pendingSince.Unix()), now)
		seedOverviewMetricForInstance(t, db, "slow-trigger", "collector", "collector@control", "moox_collector_market_fetch_timer_available", `{"space_id":"crypto","node_id":"timer-1","enabled":"true"}`, 1, now)
	})
	got, err := (Builder{Metrics: query, Now: func() time.Time { return now }}).Build(t.Context(), "")
	require.NoError(t, err)
	require.Len(t, got.BusinessChecks, 1)
	require.Equal(t, "down", got.BusinessChecks[0].Status)
	require.Contains(t, got.BusinessChecks[0].Reason, "协调失败")
}

func TestBuilderReportsStoppedCollectorForStaleTimerCoordinationSeries(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	query, _ := openOverviewState(t, func(db *gorm.DB) {
		labels := `{"space_id":"crypto","dataset_id":"bars","frequency":"1m"}`
		old := now.Add(-2 * time.Hour)
		seedOverviewMetricForInstance(t, db, "stale-required", "collector", "collector@control", "moox_collector_market_fetch_assignment_required", labels, 1, old)
		seedOverviewMetricForInstance(t, db, "stale-active", "collector", "collector@control", "moox_collector_market_fetch_assignment_active", labels, 1, old)
		seedOverviewMetricForInstance(t, db, "stale-success", "collector", "collector@control", "moox_collector_market_fetch_assignment_last_success_timestamp_seconds", `{"space_id":"crypto"}`, float64(old.Unix()), old)
	})
	got, err := (Builder{Metrics: query, Now: func() time.Time { return now }}).Build(t.Context(), "")
	require.NoError(t, err)
	require.Len(t, got.BusinessChecks, 1)
	require.Equal(t, "market_fetch", got.BusinessChecks[0].Kind)
	require.Equal(t, "down", got.BusinessChecks[0].Status)
	require.Contains(t, got.BusinessChecks[0].Reason, "停止上报")
}

func TestBuilderIgnoresDeletedTimerLabelsWhenCollectorReporterIsFresh(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query, _ := openOverviewState(t, func(db *gorm.DB) {
		require.NoError(t, db.Create(&monmetrics.MetricService{ServiceName: "collector", InstanceID: "collector@control", BootID: "boot", NodeID: "control", LastSeenAt: now}).Error)
		labels := `{"space_id":"crypto","dataset_id":"deleted-bars","frequency":"1m"}`
		old := now.Add(-2 * time.Hour)
		seedOverviewMetricForInstance(t, db, "deleted-required", "collector", "collector@control", "moox_collector_market_fetch_assignment_required", labels, 1, old)
	})
	got, err := (Builder{Metrics: query, Now: func() time.Time { return now }}).Build(t.Context(), "")
	require.NoError(t, err)
	for _, item := range got.BusinessChecks {
		require.NotEqual(t, "market_fetch", item.Kind)
	}
}

func TestBuilderReportsInitialTimerCoordinationFailure(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query, _ := openOverviewState(t, func(db *gorm.DB) {
		seedOverviewMetricForInstance(t, db, "failed-coordination", "collector", "collector@control", "moox_collector_market_fetch_coordination_healthy", `{"space_id":"crypto"}`, 0, now)
	})
	got, err := (Builder{Metrics: query, Now: func() time.Time { return now }}).Build(t.Context(), "")
	require.NoError(t, err)
	require.Len(t, got.BusinessChecks, 1)
	require.Equal(t, "down", got.BusinessChecks[0].Status)
	require.Contains(t, got.BusinessChecks[0].Reason, "协调失败")
}

func TestBuilderAggregatesTimerCoordinationAcrossCollectors(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query, _ := openOverviewState(t, func(db *gorm.DB) {
		labels := `{"space_id":"crypto"}`
		seedOverviewMetricForInstance(t, db, "coord-good", "collector", "collector@a", "moox_collector_market_fetch_coordination_healthy", labels, 1, now)
		seedOverviewMetricForInstance(t, db, "coord-bad", "collector", "collector@b", "moox_collector_market_fetch_coordination_healthy", labels, 0, now)
	})
	got, err := (Builder{Metrics: query, Now: func() time.Time { return now }}).Build(t.Context(), "")
	require.NoError(t, err)
	require.Len(t, got.BusinessChecks, 1)
	require.Equal(t, "down", got.BusinessChecks[0].Status)
	require.Contains(t, got.BusinessChecks[0].Reason, "协调失败")
}

func TestBuilderDoesNotTreatUnknownTimerReadbackAsUnavailable(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query, _ := openOverviewState(t, func(db *gorm.DB) {
		labels := `{"space_id":"crypto","dataset_id":"bars","frequency":"1m"}`
		seedOverviewMetricForInstance(t, db, "unknown-required", "collector", "collector@control", "moox_collector_market_fetch_assignment_required", labels, 1, now)
		seedOverviewMetricForInstance(t, db, "unknown-active", "collector", "collector@control", "moox_collector_market_fetch_assignment_active", labels, 1, now)
		seedOverviewMetricForInstance(t, db, "unknown-success", "collector", "collector@control", "moox_collector_market_fetch_assignment_last_success_timestamp_seconds", `{"space_id":"crypto"}`, float64(now.Unix()), now)
		seedOverviewMetricForInstance(t, db, "unknown-trigger", "collector", "collector@control", "moox_collector_market_fetch_timer_available", `{"space_id":"crypto","node_id":"timer-1","enabled":"true"}`, -1, now)
	})
	got, err := (Builder{Metrics: query, Now: func() time.Time { return now }}).Build(t.Context(), "")
	require.NoError(t, err)
	require.Len(t, got.BusinessChecks, 1)
	require.Equal(t, "healthy", got.BusinessChecks[0].Status)
}

func TestBuilderPlacementFailureOverridesFreshReporter(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query, repositories := openOverviewState(t, func(db *gorm.DB) {
		if err := db.Create(&monmetrics.MetricService{
			ServiceName: "storage-primary", InstanceID: "storage@node-a", BootID: "boot-a",
			NodeID: "node-a", LastSeenAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
	})
	check := domain.Check{
		SpaceID: "", CheckID: "placement:node-a:storage-primary",
		Name: "storage-primary@node-a", Kind: domain.CheckKindHTTP,
		Source: domain.CheckSourcePlacement, Enabled: true,
		Labels: `{"host_id":"node-a","component_id":"storage-primary"}`,
	}
	if err := repositories.Checks.Create(t.Context(), &check); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Results.Insert(t.Context(), &domain.CheckResult{
		ResultID: "storage-down", SpaceID: check.SpaceID, CheckID: check.CheckID,
		Status: domain.CheckStatusDown, ErrorMessage: "readyz failed", CheckedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	seedServiceTopology(t, repositories, "node-a", "storage-primary", true)
	got, err := (Builder{
		Metrics: query, Checks: repositories.Checks, Topology: repositories.Topology, Results: repositories.Results,
	}).Build(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Services) != 1 || got.Services[0].Status != "down" ||
		!strings.Contains(got.Services[0].Reason, "readyz failed") ||
		got.Services[0].ReporterStatus != "healthy" {
		t.Fatalf("services = %+v", got.Services)
	}
}

func TestBuilderIncludesPlacementServiceWithoutReporter(t *testing.T) {
	query, repositories := openOverviewState(t, func(*gorm.DB) {})
	check := domain.Check{
		SpaceID: "", CheckID: "placement:node-b:factor-mgr",
		Name: "factor-mgr@node-b", Kind: domain.CheckKindHTTP,
		Source: domain.CheckSourcePlacement, Enabled: true,
		Labels: `{"host_id":"node-b","component_id":"factor-mgr"}`,
	}
	if err := repositories.Checks.Create(t.Context(), &check); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Results.Insert(t.Context(), &domain.CheckResult{
		ResultID: "factor-ready", SpaceID: check.SpaceID, CheckID: check.CheckID,
		Success: true, Status: domain.CheckStatusOK, CheckedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	seedServiceTopology(t, repositories, "node-b", "factor-mgr", true)
	got, err := (Builder{
		Metrics: query, Checks: repositories.Checks, Topology: repositories.Topology, Results: repositories.Results,
	}).Build(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Services) != 1 || got.Services[0].Status != "unknown" ||
		got.Services[0].ReporterStatus != "missing" ||
		!strings.Contains(got.Services[0].Reason, "reporter missing") {
		t.Fatalf("services = %+v", got.Services)
	}
}

func TestBuilderDoesNotRequireReporterFromHealthOnlyService(t *testing.T) {
	query, repositories := openOverviewState(t, func(*gorm.DB) {})
	check := domain.Check{
		SpaceID: "", CheckID: "placement:node-b:web-host",
		Name: "web-host@node-b", Kind: domain.CheckKindHTTP,
		Source: domain.CheckSourcePlacement, Enabled: true,
		Labels: `{"host_id":"node-b","component_id":"web-host"}`,
	}
	if err := repositories.Checks.Create(t.Context(), &check); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Results.Insert(t.Context(), &domain.CheckResult{
		ResultID: "web-ready", SpaceID: check.SpaceID, CheckID: check.CheckID,
		Success: true, Status: domain.CheckStatusOK, CheckedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	seedServiceTopology(t, repositories, "node-b", "web-host", true)
	got, err := (Builder{Metrics: query, Checks: repositories.Checks, Topology: repositories.Topology, Results: repositories.Results}).Build(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Services) != 1 || got.Services[0].Status != "healthy" || got.Services[0].ReporterStatus != "" {
		t.Fatalf("services = %+v", got.Services)
	}
}

func TestBuilderMarksBalanceDownAfterThreeFailures(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query := openOverviewMetrics(t, func(db *gorm.DB) {
		seedOverviewMetric(t, db, "balance-success", "moox_trade_balance_sync_last_success_timestamp_seconds", `{}`, float64(now.Add(-8*time.Minute).Unix()), now)
		seedOverviewMetric(t, db, "balance-run", "moox_trade_balance_sync_last_run_timestamp_seconds", `{}`, float64(now.Unix()), now)
		seedOverviewMetric(t, db, "balance-failures", "moox_trade_balance_sync_consecutive_failures", `{}`, 3, now)
		seedOverviewMetric(t, db, "balance-difference", "moox_trade_balance_sync_max_difference_ratio", `{}`, 0.01, now)
	})
	got, err := (Builder{Metrics: query, Now: func() time.Time { return now }, BalanceDifferenceThreshold: 0.05}).Build(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.BusinessChecks) != 1 || got.BusinessChecks[0].Status != "down" ||
		!strings.Contains(got.BusinessChecks[0].Reason, "3 consecutive") {
		t.Fatalf("balance status = %+v", got.BusinessChecks)
	}
}

func TestBuilderOmitsBalanceCheckBeforeAnyAccountSyncRuns(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query := openOverviewMetrics(t, func(db *gorm.DB) {
		seedOverviewMetric(t, db, "balance-success", "moox_trade_balance_sync_last_success_timestamp_seconds", `{}`, 0, now)
		seedOverviewMetric(t, db, "balance-run", "moox_trade_balance_sync_last_run_timestamp_seconds", `{}`, 0, now)
		seedOverviewMetric(t, db, "balance-failures", "moox_trade_balance_sync_consecutive_failures", `{}`, 0, now)
		seedOverviewMetric(t, db, "balance-difference", "moox_trade_balance_sync_max_difference_ratio", `{}`, 0, now)
	})
	got, err := (Builder{Metrics: query, Now: func() time.Time { return now }}).Build(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.BusinessChecks) != 0 {
		t.Fatalf("balance status = %+v, want no check before the first sync run", got.BusinessChecks)
	}
}

func TestBuilderMarksBalanceDifferenceAboveThresholdDown(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query := openOverviewMetrics(t, func(db *gorm.DB) {
		seedOverviewMetric(t, db, "balance-success", "moox_trade_balance_sync_last_success_timestamp_seconds", `{}`, float64(now.Unix()), now)
		seedOverviewMetric(t, db, "balance-run", "moox_trade_balance_sync_last_run_timestamp_seconds", `{}`, float64(now.Unix()), now)
		seedOverviewMetric(t, db, "balance-failures", "moox_trade_balance_sync_consecutive_failures", `{}`, 0, now)
		seedOverviewMetric(t, db, "balance-difference", "moox_trade_balance_sync_max_difference_ratio", `{}`, 0.08, now)
	})
	got, err := (Builder{Metrics: query, Now: func() time.Time { return now }, BalanceDifferenceThreshold: 0.05}).Build(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.BusinessChecks) != 1 || got.BusinessChecks[0].Status != "down" ||
		!strings.Contains(got.BusinessChecks[0].Reason, "exceeds") {
		t.Fatalf("balance status = %+v", got.BusinessChecks)
	}
}

func TestBuilderIgnoresDatasetEnabledFromSupersededFactorBoot(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 30, 0, 0, time.UTC)
	oldBoot := "af0221ff-91e9-4cb2-b1e3-00d434de7bf1"
	newBoot := "200c376c-2528-4ac9-a17b-f443b3fa49c0"
	retired := `{"dataset_id":"dataset_factor_binance_kline_1m","freq":"1m","space_id":"crypto"}`
	live := `{"dataset_id":"dataset_factor_binance_kline_1m","freq":"1m","space_id":"crypto"}`
	query := openOverviewMetrics(t, func(db *gorm.DB) {
		require.NoError(t, db.Create([]monmetrics.MetricService{
			{
				ServiceName: "factor-mgr", InstanceID: "factor-mgr@control", NodeID: "control",
				BootID: oldBoot, Version: "sha256:old", LastSeenAt: now, CreatedAt: now.Add(-72 * time.Hour),
			},
			{
				ServiceName: "factor-mgr", InstanceID: "factor-mgr@control", NodeID: "control",
				BootID: newBoot, Version: "sha256:new", LastSeenAt: now.Add(-time.Second), CreatedAt: now.Add(-time.Minute),
			},
		}).Error)
		seedOverviewMetricForInstance(t, db, "retired-enabled", "factor-mgr", "factor-mgr@control", "moox_factor_dataset_enabled", retired, 1, now)
		require.NoError(t, db.Model(&monmetrics.MetricLatest{}).Where("c_series_id = ?", "retired-enabled").Updates(map[string]any{
			"c_message_id":       oldBoot + "-00000000000000009261",
			"c_producer_version": "sha256:old",
		}).Error)
		seedOverviewMetricForInstance(t, db, "retired-interval", "factor-mgr", "factor-mgr@control", "moox_factor_dataset_expected_interval_seconds", retired, 60, now)
		seedOverviewMetricForInstance(t, db, "retired-run", "factor-mgr", "factor-mgr@control", "moox_factor_dataset_last_run_timestamp_seconds", retired, float64(now.Add(-3*24*time.Hour).Unix()), now)
		seedOverviewMetricForInstance(t, db, "live-enabled", "factor-mgr", "factor-mgr@control", "moox_factor_dataset_enabled", live, 1, now)
		require.NoError(t, db.Model(&monmetrics.MetricLatest{}).Where("c_series_id = ?", "live-enabled").Updates(map[string]any{
			"c_message_id":       newBoot + "-00000000000000000007",
			"c_producer_version": "sha256:new",
		}).Error)
		seedOverviewMetricForInstance(t, db, "live-interval", "factor-mgr", "factor-mgr@control", "moox_factor_dataset_expected_interval_seconds", live, 60, now)
		seedOverviewMetricForInstance(t, db, "inventory", "factor-mgr", "factor-mgr@control", "moox_factor_dataset_inventory_last_success_timestamp_seconds", `{}`, float64(now.Unix()), now)
	})
	got, err := (Builder{Metrics: query, Now: func() time.Time { return now }, Policy: testRealtimePolicy()}).Build(t.Context(), "crypto")
	require.NoError(t, err)
	var ids []string
	for _, item := range got.Datasets {
		if item.Producer == "factor" {
			ids = append(ids, item.DatasetID)
		}
	}
	require.Equal(t, []string{"dataset_factor_binance_kline_1m"}, ids)
}

func TestBuilderIncludesStorageCommitFactsWithoutEnabledInventory(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	labels := `{"dataset_id":"market_kline","freq":"1m","space_id":"crypto"}`
	query := openOverviewMetrics(t, func(db *gorm.DB) {
		seedOverviewMetric(t, db, "collector-enabled", "moox_collector_dataset_enabled", labels, 1, now)
		seedOverviewMetric(t, db, "collector-interval", "moox_collector_dataset_expected_interval_seconds", labels, 300, now)
		seedOverviewMetric(t, db, "collector-inventory", "moox_collector_dataset_inventory_last_success_timestamp_seconds", `{}`, float64(now.Unix()), now)
		seedOverviewMetric(t, db, "collector-run", "moox_collector_dataset_last_run_timestamp_seconds", labels, float64(now.Unix()), now)
		seedOverviewMetric(t, db, "collector-success", "moox_collector_dataset_last_success_timestamp_seconds", labels, float64(now.Unix()), now)
		seedOverviewMetric(t, db, "collector-output", "moox_collector_dataset_output_watermark_timestamp_seconds", labels, float64(now.Unix()), now)
		seedOverviewMetric(t, db, "factor-enabled", "moox_factor_dataset_enabled", labels, 1, now)
		seedOverviewMetric(t, db, "factor-interval", "moox_factor_dataset_expected_interval_seconds", labels, 600, now)
		seedOverviewMetric(t, db, "factor-inventory", "moox_factor_dataset_inventory_last_success_timestamp_seconds", `{}`, float64(now.Unix()), now)
		seedOverviewMetric(t, db, "factor-run", "moox_factor_dataset_last_run_timestamp_seconds", labels, float64(now.Unix()), now)
		seedOverviewMetric(t, db, "factor-success", "moox_factor_dataset_last_success_timestamp_seconds", labels, float64(now.Unix()), now)
		seedOverviewMetric(t, db, "factor-output", "moox_factor_dataset_output_watermark_timestamp_seconds", labels, float64(now.Unix()), now)
		storageAt := now.Add(-3 * time.Minute)
		seedOverviewMetric(t, db, "storage-run", "moox_storage_dataset_last_run_timestamp_seconds", labels, float64(storageAt.Unix()), now)
		seedOverviewMetric(t, db, "storage-success", "moox_storage_dataset_last_success_timestamp_seconds", labels, float64(storageAt.Unix()), now)
		seedOverviewMetric(t, db, "storage-output", "moox_storage_dataset_output_watermark_timestamp_seconds", labels, float64(storageAt.Unix()), now)
	})
	got, err := (Builder{Metrics: query, Now: func() time.Time { return now }, Policy: testRealtimePolicy()}).Build(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	var storageRow *DatasetFrequencyStatus
	for i := range got.Datasets {
		if got.Datasets[i].Producer == "storage" {
			storageRow = &got.Datasets[i]
		}
	}
	if len(got.Datasets) != 3 || storageRow == nil ||
		storageRow.DatasetID != "market_kline" || storageRow.Status != "healthy" {
		t.Fatalf("Storage fact row = %+v", got.Datasets)
	}
}

func TestBuilderReportsStorageViewWatermarkAsIndependentDataset(t *testing.T) {
	now := time.Date(2026, 8, 20, 15, 0, 0, 0, time.UTC)
	watermark := now.Add(-20 * time.Minute)
	// 指标目录按规范化（键排序）的标签 JSON 匹配同一身份的序列。
	labels := `{"freq":"1m","space_id":"crypto","view_id":"view_crypto_kline_1m_factor"}`
	query := openOverviewMetrics(t, func(db *gorm.DB) {
		seedOverviewMetricForInstance(t, db, "factor-view-watermark", "storage-view", "storage-view@control", "moox_storage_view_output_watermark_timestamp_seconds", labels, float64(watermark.Unix()), now)
		seedOverviewMetricForInstance(t, db, "factor-view-arrival", "storage-view", "storage-view@control", "moox_storage_view_output_arrival_timestamp_seconds", labels, float64(watermark.Add(time.Minute+4*time.Second).Unix()), now)
	})
	got, err := (Builder{Metrics: query, Inventory: cryptoInventory, Now: func() time.Time { return now }, Policy: testRealtimePolicy()}).Build(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Datasets) != 1 {
		t.Fatalf("datasets = %+v", got.Datasets)
	}
	row := got.Datasets[0]
	if row.Producer != "storage_view" || row.DatasetID != "view_crypto_kline_1m_factor" || row.Freq != "1m" || row.Status != "stale" {
		t.Fatalf("storage view status = %+v", row)
	}
	// 水位 14:40 那根 14:41 收线，之后的 14:41 那根 14:42 收线，到 15:00 已等待 18 分钟；视图写完的时刻作为到达时间。
	if !row.OutputWatermarkAt.Equal(watermark) || row.WaitingBars != 19 || row.OverdueSeconds != int64((18*time.Minute).Seconds()) {
		t.Fatalf("storage view watermark = %+v", row)
	}
	if !row.DelayKnown || row.DelaySeconds != 4 {
		t.Fatalf("storage view arrival = %+v", row)
	}
	if !row.LateConfirmed || !row.ExpectedBarAt.Equal(now.Add(-time.Minute)) {
		t.Fatalf("storage view expected bar = %+v", row)
	}
}

// 水位是生产者在上报那一刻的快照：预期 K 线在那次上报之后才收线，还不知道它是否已到，不判过期。
func TestBuilderDoesNotCountBarsClosedAfterTheLastReport(t *testing.T) {
	// 指标目录按真实时钟判断上报是否中断，所以用当前时间：整分 + 40 秒，上一次上报在整分前 5 秒。
	minute := time.Now().UTC().Truncate(time.Minute)
	now, reported := minute.Add(40*time.Second), minute.Add(-5*time.Second)
	labels := `{"freq":"1m","space_id":"crypto","view_id":"view_crypto_kline_1m"}`
	query := openOverviewMetrics(t, func(db *gorm.DB) {
		seedOverviewMetricForInstance(t, db, "view-watermark", "storage-view", "storage-view@control", "moox_storage_view_output_watermark_timestamp_seconds", labels, float64(minute.Add(-2*time.Minute).Unix()), reported)
	})
	policy := testRealtimePolicy()
	policy.Defaults.MinuteLateness = 30 * time.Second
	got, err := (Builder{Metrics: query, Inventory: cryptoInventory, Now: func() time.Time { return now }, Policy: policy}).Build(t.Context(), "")
	require.NoError(t, err)
	require.Len(t, got.Datasets, 1)
	row := got.Datasets[0]
	require.Equal(t, "healthy", row.Status, row.Reason)
	require.Equal(t, 1, row.WaitingBars)
	require.EqualValues(t, 40, row.OverdueSeconds, "已晚按当前时间算，超过允许迟到但还没有收线后的上报，不判过期")
	require.False(t, row.LateConfirmed)
	require.True(t, row.ExpectedBarAt.Equal(minute.Add(-time.Minute)))
}

func TestBuilderAggregatesDatasetInstances(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	labels := `{"dataset_id":"market_kline","freq":"1m","space_id":"crypto"}`
	query := openOverviewMetrics(t, func(db *gorm.DB) {
		for _, instance := range []struct {
			id        string
			seenAt    time.Time
			lastRun   time.Time
			inventory time.Time
		}{
			{id: "collector-a", seenAt: now.Add(-10 * time.Minute), lastRun: now.Add(-10 * time.Minute), inventory: now.Add(-10 * time.Minute)},
			{id: "collector-b", seenAt: now, lastRun: now, inventory: now},
		} {
			seedOverviewMetricForInstance(t, db, instance.id+"-enabled", "collector", instance.id, "moox_collector_dataset_enabled", labels, 1, instance.seenAt)
			seedOverviewMetricForInstance(t, db, instance.id+"-interval", "collector", instance.id, "moox_collector_dataset_expected_interval_seconds", labels, 60, instance.seenAt)
			seedOverviewMetricForInstance(t, db, instance.id+"-inventory", "collector", instance.id, "moox_collector_dataset_inventory_last_success_timestamp_seconds", `{}`, float64(instance.inventory.Unix()), instance.seenAt)
			seedOverviewMetricForInstance(t, db, instance.id+"-run", "collector", instance.id, "moox_collector_dataset_last_run_timestamp_seconds", labels, float64(instance.lastRun.Unix()), instance.seenAt)
			seedOverviewMetricForInstance(t, db, instance.id+"-success", "collector", instance.id, "moox_collector_dataset_last_success_timestamp_seconds", labels, float64(instance.lastRun.Unix()), instance.seenAt)
			seedOverviewMetricForInstance(t, db, instance.id+"-output", "collector", instance.id, "moox_collector_dataset_output_watermark_timestamp_seconds", labels, float64(instance.lastRun.Unix()), instance.seenAt)
		}
	})
	got, err := (Builder{
		Metrics: query, Now: func() time.Time { return now }, Policy: testRealtimePolicy(),
	}).Build(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Datasets) != 1 || got.Datasets[0].Status != "healthy" ||
		!got.Datasets[0].LastRunAt.Equal(now) {
		t.Fatalf("datasets = %+v", got.Datasets)
	}
}

func TestDatasetFrequencyLimitReturnsErrorInsteadOfTruncating(t *testing.T) {
	err := ensureDatasetLimit(MaxDatasetFrequencyStatuses + 1)
	if err == nil || !strings.Contains(err.Error(), "exceed limit 1000") {
		t.Fatalf("limit error = %v", err)
	}
}

func TestBuilderReturnsBoundedEmptyOverviewWhenSourcesAreDisabled(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	got, err := (Builder{Now: func() time.Time { return now }}).Build(t.Context(), "mooxsys")
	if err != nil {
		t.Fatal(err)
	}
	if !got.GeneratedAt.Equal(now) || len(got.Services) != 0 || len(got.Hosts) != 0 || len(got.Datasets) != 0 {
		t.Fatalf("overview = %+v", got)
	}
}

func TestOverviewReportsStorageOutboxBacklogBeforeDatasetWatermarkStalls(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query := openOverviewMetrics(t, func(db *gorm.DB) {
		seedOverviewMetricForInstance(t, db, "outbox-pending", "storage-node", "storage-node@storage", "moox_storage_outbox_pending_entries", `{}`, 42, now)
		seedOverviewMetricForInstance(t, db, "outbox-age", "storage-node", "storage-node@storage", "moox_storage_outbox_oldest_age_seconds", `{}`, 181, now)
	})
	overview, err := (Builder{Metrics: query, Now: func() time.Time { return now }}).Build(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	var found *BusinessStatus
	for i := range overview.BusinessChecks {
		if overview.BusinessChecks[i].Kind == "data_delivery" && overview.BusinessChecks[i].Module == "storage_outbox" {
			found = &overview.BusinessChecks[i]
			break
		}
	}
	if found == nil || found.Status != "down" || !strings.Contains(found.Reason, "42") || !strings.Contains(found.Reason, "3 分钟") {
		t.Fatalf("storage outbox health = %+v, checks=%+v", found, overview.BusinessChecks)
	}
}

func TestOverviewKeepsShortStorageOutboxBacklogHealthy(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query := openOverviewMetrics(t, func(db *gorm.DB) {
		seedOverviewMetricForInstance(t, db, "outbox-pending", "storage-node", "storage-node@storage", "moox_storage_outbox_pending_entries", `{}`, 3, now)
		seedOverviewMetricForInstance(t, db, "outbox-age", "storage-node", "storage-node@storage", "moox_storage_outbox_oldest_age_seconds", `{}`, 20, now)
	})
	overview, err := (Builder{Metrics: query, Now: func() time.Time { return now }}).Build(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range overview.BusinessChecks {
		if item.Kind == "data_delivery" && item.Module == "storage_outbox" && item.Status != "healthy" {
			t.Fatalf("storage outbox health = %+v", item)
		}
	}
}

func TestBuilderAlertsOnStockCNProviderFeedFailureRate(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query := openOverviewMetrics(t, func(db *gorm.DB) {
		labels := `{"market_id":"stockcn","route_id":"stockcn_equity_kline_1m_v4","provider_id":"sina","feed_kind":"kline","group_id":"0","batch_kind":"realtime","result":"success"}`
		seedOverviewMetricForInstance(t, db, "feed-success", "scf-collector", "stock-fetch-0", "moox_collector_market_feed_results_total", labels, 8, now)
		labels = `{"market_id":"stockcn","route_id":"stockcn_equity_kline_1m_v4","provider_id":"sina","feed_kind":"kline","group_id":"0","batch_kind":"realtime","result":"http_5xx"}`
		seedOverviewMetricForInstance(t, db, "feed-failure", "scf-collector", "stock-fetch-0", "moox_collector_market_feed_results_total", labels, 3, now)
	})
	overview, err := (Builder{Metrics: query, Now: func() time.Time { return now }}).Build(t.Context(), "stockcn")
	require.NoError(t, err)
	var found *BusinessStatus
	for index := range overview.BusinessChecks {
		if overview.BusinessChecks[index].Module == "provider_feed:sina" {
			found = &overview.BusinessChecks[index]
			break
		}
	}
	require.NotNil(t, found)
	require.Equal(t, "down", found.Status)
	require.Contains(t, found.Reason, "失败率")
}

func TestBuilderAcceptsCompleteStockCNInstrumentSnapshot(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query := openOverviewMetrics(t, func(db *gorm.DB) {
		base := `{"market_id":"stockcn","route_id":"stockcn_instrument_v1","provider_id":"sina","result":"success"}`
		seedOverviewMetricForInstance(t, db, "instrument-snapshot", "scf-collector", "stock-instrument", "moox_collector_market_instrument_last_snapshot_timestamp_seconds", base, float64(now.Unix()), now)
		seedOverviewMetricForInstance(t, db, "instrument-active", "scf-collector", "stock-instrument", "moox_collector_market_instrument_active", base, 5180, now)
		for _, exchange := range []string{"XSHG", "XSHE", "XBSE"} {
			labels := `{"market_id":"stockcn","route_id":"stockcn_instrument_v1","provider_id":"sina","exchange":"` + exchange + `"}`
			seedOverviewMetricForInstance(t, db, "instrument-"+exchange, "scf-collector", "stock-instrument", "moox_collector_market_instrument_exchange", labels, 1, now)
		}
	})
	overview, err := (Builder{Metrics: query, Now: func() time.Time { return now }}).Build(t.Context(), "stockcn")
	require.NoError(t, err)
	var found *BusinessStatus
	for index := range overview.BusinessChecks {
		if overview.BusinessChecks[index].Module == "instrument_snapshot:sina" {
			found = &overview.BusinessChecks[index]
			break
		}
	}
	require.NotNil(t, found)
	require.Equal(t, "healthy", found.Status)
}

func openOverviewMetrics(t *testing.T, seed func(*gorm.DB)) *monmetrics.QueryService {
	t.Helper()
	query, _ := openOverviewState(t, seed)
	return query
}

func openOverviewState(t *testing.T, seed func(*gorm.DB)) (*monmetrics.QueryService, *store.Repositories) {
	t.Helper()
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	query, err := store.WithDatabase(manager, func(db *gorm.DB) *monmetrics.QueryService {
		seed(db)
		return monmetrics.NewQueryService(monmetrics.NewMetricMessageStore(db), nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	return query, manager.Repositories()
}

func seedOverviewMetric(t *testing.T, db *gorm.DB, id, name, labels string, value float64, observedAt time.Time) {
	t.Helper()
	seedOverviewMetricForInstance(t, db, id, "cloudnode", "cloudnode@node-a", name, labels, value, observedAt)
}

func seedOverviewMetricForInstance(
	t *testing.T,
	db *gorm.DB,
	id, serviceName, instanceID, name, labels string,
	value float64,
	observedAt time.Time,
) {
	t.Helper()
	if err := db.Create(&monmetrics.MetricSeries{
		ServiceName: serviceName, InstanceID: instanceID, SeriesID: id,
		MetricName: name, MetricType: "gauge", LabelsJSON: labels, LastSeenAt: observedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&monmetrics.MetricLatest{
		SeriesID: id, ServiceName: serviceName, InstanceID: instanceID,
		MetricName: name, MetricType: "gauge", LabelsJSON: labels, Value: value, ObservedAt: observedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
}

func seedServiceTopology(t *testing.T, repositories *store.Repositories, hostID, componentID string, enabled bool) {
	t.Helper()
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	status := servicecatalog.Enabled
	if !enabled {
		status = servicecatalog.Disabled
	}
	snapshot := domain.TopologySnapshot{Catalog: catalog, ObservedAt: time.Now().UTC(), Hosts: []domain.TopologyHost{{HostID: hostID, Address: "host.example.test", Status: servicecatalog.Enabled}}, Placements: []domain.TopologyPlacement{{HostID: hostID, ComponentID: componentID, Status: status}}}
	// Preserve the fixture's existing probe definitions when registering topology.
	checks, err := repositories.Checks.List(t.Context(), store.ListChecksOptions{Source: domain.CheckSourcePlacement, Page: store.Page{PageSize: 500}})
	require.NoError(t, err)
	_, err = repositories.Topology.Reconcile(t.Context(), snapshot, checks)
	require.NoError(t, err)
}

// 生产者按设计只产出部分结果（空洞保护、历史不足、预热）时，数据集显示降级与“积累中”，而不是“尚无成功运行 / 成功过期”；
// 只有部分结果仍按期出现才算积累，部分结果也断了就回到原来的判断，停掉的生产者不会被误当成积累。
func TestDatasetStatusReportsWarmingWhilePartialRunsKeepArriving(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	key := datasetKey{producer: "factor", spaceID: "crypto", datasetID: "dataset_factor_1m", freq: "1m"}
	policy := testRealtimePolicy()
	at := func(ago time.Duration) float64 { return float64(now.Add(-ago).Unix()) }
	base := datasetValues{interval: 60, inventory: float64(now.Unix()), lastRun: at(10 * time.Second)}

	never := base
	never.lastPartial = at(10 * time.Second)
	got := datasetStatus(now, key, never, policy, cryptoClock)
	require.Equal(t, "degraded", got.Status)
	require.Equal(t, DatasetReasonWarming, got.Reason, "从未成功过但部分结果在持续产出")
	require.Equal(t, now.Add(-10*time.Second), got.LastPartialAt)

	afterSuccess := base
	afterSuccess.lastSuccess, afterSuccess.lastPartial = at(2*time.Hour), at(10*time.Second)
	got = datasetStatus(now, key, afterSuccess, policy, cryptoClock)
	require.Equal(t, DatasetReasonWarming, got.Reason, "原来成功过、数据重置后重新积累")

	recovered := base
	recovered.lastSuccess, recovered.lastPartial = at(10*time.Second), at(5*time.Minute)
	got = datasetStatus(now, key, recovered, policy, cryptoClock)
	require.Equal(t, "healthy", got.Status, "最近一次是成功就恢复健康")

	partialStopped := base
	partialStopped.lastSuccess, partialStopped.lastPartial = at(3*time.Hour), at(30*time.Minute)
	got = datasetStatus(now, key, partialStopped, policy, cryptoClock)
	require.Equal(t, "success stale", got.Reason, "部分结果也断了，回到成功过期")

	producerStopped := datasetValues{interval: 60, inventory: float64(now.Unix()), lastRun: at(10 * time.Minute), lastPartial: at(10 * time.Minute)}
	got = datasetStatus(now, key, producerStopped, policy, cryptoClock)
	require.Equal(t, "run stale", got.Reason, "生产者停了仍然是 stale，不会被当成积累")

	noPartial := base
	got = datasetStatus(now, key, noPartial, policy, cryptoClock)
	require.Equal(t, "尚无成功运行", got.Reason, "没有部分结果上报时保持原样")
}

// 检查对象已停用或移除时仍写入通过结果（让既有告警恢复），概览要显示“已停用”，不能算作正常。
func TestBusinessCheckRetiredResultIsDisabled(t *testing.T) {
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	require.NoError(t, manager.ApplySchema(schema.SQL()))
	repos := manager.Repositories()
	now := time.Now().UTC()
	checkID := "kline_freshness:stockcn:view_a:1m"
	require.NoError(t, repos.Checks.Create(t.Context(), &domain.Check{SpaceID: "stockcn", CheckID: checkID, Name: "A 股分钟K线", Kind: domain.CheckKindExternal, Enabled: true}))
	require.NoError(t, repos.Results.Insert(t.Context(), &domain.CheckResult{ResultID: "retired", SpaceID: "stockcn", CheckID: checkID, Status: domain.CheckStatusOK, Success: true, ErrorMessage: domain.CheckReasonRetired, CheckedAt: now}))
	got, err := (Builder{Checks: repos.Checks, Results: repos.Results, Now: func() time.Time { return now }}).Build(t.Context(), "")
	require.NoError(t, err)
	require.Len(t, got.BusinessChecks, 1)
	require.Equal(t, "disabled", got.BusinessChecks[0].Status)
}

// collector-stream 心跳中断超过 3 分钟、连续 3 个周期有失败标的（不含疑似下架）时告警。
func TestBuilderReportsCollectorStreamHeartbeatAndMissingStreaks(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query, _ := openOverviewState(t, func(db *gorm.DB) {
		seedOverviewMetricForInstance(t, db, "stream-hb", "collector", "collector@control", "moox_collector_stream_heartbeat_timestamp_seconds", `{"space_id":"crypto"}`, float64(now.Add(-4*time.Minute).Unix()), now)
		seedOverviewMetricForInstance(t, db, "stream-streak", "collector", "collector@control", "moox_collector_stream_failed_period_streak", `{"space_id":"crypto","market":"swap","frequency":"1m","dataset_id":"ds_swap"}`, 3, now)
		seedOverviewMetricForInstance(t, db, "stream-ok", "collector", "collector@control", "moox_collector_stream_failed_period_streak", `{"space_id":"crypto","market":"spot","frequency":"1m","dataset_id":"ds_spot"}`, 2, now)
	})
	got, err := (Builder{Metrics: query, Now: func() time.Time { return now }}).Build(t.Context(), "")
	require.NoError(t, err)
	byModule := map[string]BusinessStatus{}
	for _, check := range got.BusinessChecks {
		byModule[check.Module] = check
	}
	require.Equal(t, "down", byModule["collector_stream"].Status)
	require.Contains(t, byModule["collector_stream"].Reason, "心跳已中断")
	require.Equal(t, "down", byModule["collector_stream_missing"].Status)
	require.Contains(t, byModule["collector_stream_missing"].Reason, "swap/ds_swap 1m 连续 3 期")
	require.NotContains(t, byModule["collector_stream_missing"].Reason, "ds_spot")

	fresh, _ := openOverviewState(t, func(db *gorm.DB) {
		seedOverviewMetricForInstance(t, db, "stream-hb", "collector", "collector@control", "moox_collector_stream_heartbeat_timestamp_seconds", `{"space_id":"crypto"}`, float64(now.Add(-20*time.Second).Unix()), now)
	})
	got, err = (Builder{Metrics: fresh, Now: func() time.Time { return now }}).Build(t.Context(), "")
	require.NoError(t, err)
	for _, check := range got.BusinessChecks {
		require.Equal(t, "healthy", check.Status, check.Module)
	}
}

// 因子集合连续水位阻塞超过 30 分钟告警，未超过或没有阻塞时正常。
func TestBuilderReportsStalledFactorWatermarks(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query, _ := openOverviewState(t, func(db *gorm.DB) {
		seedOverviewMetricForInstance(t, db, "wm-stalled", "factor", "factor-mgr@control", "moox_factor_set_watermark_blocked_since_timestamp_seconds", `{"space_id":"crypto","set_id":"fset_a"}`, float64(now.Add(-31*time.Minute).Unix()), now)
		seedOverviewMetricForInstance(t, db, "wm-recent", "factor", "factor-mgr@control", "moox_factor_set_watermark_blocked_since_timestamp_seconds", `{"space_id":"crypto","set_id":"fset_b"}`, float64(now.Add(-5*time.Minute).Unix()), now)
		seedOverviewMetricForInstance(t, db, "wm-ok", "factor", "factor-mgr@control", "moox_factor_set_watermark_blocked_since_timestamp_seconds", `{"space_id":"crypto","set_id":"fset_c"}`, 0, now)
	})
	got, err := (Builder{Metrics: query, Now: func() time.Time { return now }}).Build(t.Context(), "")
	require.NoError(t, err)
	var check *BusinessStatus
	for i := range got.BusinessChecks {
		if got.BusinessChecks[i].Kind == "factor_watermark" {
			check = &got.BusinessChecks[i]
		}
	}
	require.NotNil(t, check)
	require.Equal(t, "down", check.Status)
	require.Contains(t, check.Reason, "fset_a")
	require.NotContains(t, check.Reason, "fset_b")
}
