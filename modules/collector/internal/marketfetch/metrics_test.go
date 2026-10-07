package marketfetch

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

type operationalStatsFake struct {
	stats store.OperationalStats
	err   error
}

func (f *operationalStatsFake) OperationalStats(context.Context, string, time.Time, int) (store.OperationalStats, error) {
	return f.stats, f.err
}

func TestOperationalMetricsZeroResetBoundedLabelsAndFailedRefresh(t *testing.T) {
	registry := prometheus.NewRegistry()
	m := NewMetrics(registry)
	now := time.Now().UTC().Truncate(time.Second)
	f := &operationalStatsFake{stats: store.OperationalStats{DBBytes: 100, WALBytes: 20, Rows: map[string]store.OperationalRowCount{"retry": {Count: 9, Capped: true}, "task-secret": {Count: 1}}, OldestPendingRetry: now.Add(-time.Hour)}}
	require.NoError(t, m.RefreshOperationalStats(context.Background(), f, "crypto", now.Add(-24*time.Hour), now))
	require.Equal(t, 9.0, testutil.ToFloat64(m.storeRows.WithLabelValues("crypto", "retry")))
	require.Equal(t, 1.0, testutil.ToFloat64(m.storeRowsCapped.WithLabelValues("crypto", "retry")))
	f.err = errors.New("task-secret query failure")
	require.Error(t, m.RefreshOperationalStats(context.Background(), f, "crypto", now, now.Add(time.Hour)))
	require.Equal(t, 9.0, testutil.ToFloat64(m.storeRows.WithLabelValues("crypto", "retry")))
	require.Equal(t, 0.0, testutil.ToFloat64(m.storeStatsHealthy.WithLabelValues("crypto")))
	require.Equal(t, float64(now.Unix()), testutil.ToFloat64(m.storeStatsLastSuccess.WithLabelValues("crypto")))
	f.err, f.stats = nil, store.OperationalStats{}
	require.NoError(t, m.RefreshOperationalStats(context.Background(), f, "crypto", now, now.Add(time.Hour)))
	require.Zero(t, testutil.ToFloat64(m.storeRows.WithLabelValues("crypto", "retry")))
	require.Zero(t, testutil.ToFloat64(m.storeOldest.WithLabelValues("crypto", "retry_pending")))
	m.ObserveMaintenanceDeletes("crypto", map[string]int64{"runs": 2, "period_readiness": 4, "task-secret": 3})
	require.Equal(t, 4.0, testutil.ToFloat64(m.maintenanceDeleted.WithLabelValues("crypto", "period_readiness")))
	m.ObserveMaintenanceDeletes("crypto", nil)
	require.Zero(t, testutil.ToFloat64(m.maintenanceDeleted.WithLabelValues("crypto", "runs")))
	require.Zero(t, testutil.ToFloat64(m.maintenanceDeleted.WithLabelValues("crypto", "period_readiness")))
	m.ObserveSchedulerSuccess("crypto", "task-secret", now)
	m.ObserveSchedulerSuccess("crypto", "planning", now)
	m.ObserveSchedulerSuccess("crypto", "dispatch", now)
	m.ObserveMaintenancePass(time.Second, errors.New("task-secret"))
	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			for name, value := range metricLabels(metric) {
				require.NotContains(t, name, "subject")
				require.NotContains(t, name, "task")
				require.NotEqual(t, "task-secret", value)
			}
		}
	}
}

func TestPeriodPendingMetricsUseBoundedSpaceAndFrequencyLabels(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewMetrics(registry)
	metrics.ObservePeriodPendingSnapshot("crypto", map[string]int{"1m": 2, "5m": 1})
	require.Equal(t, 2.0, testutil.ToFloat64(metrics.periodPending.WithLabelValues("crypto", "1m")))
	require.Equal(t, 1.0, testutil.ToFloat64(metrics.periodPending.WithLabelValues("crypto", "5m")))

	metrics.ObservePeriodPendingSnapshot("crypto", map[string]int{"1m": 1})
	require.Equal(t, 1.0, testutil.ToFloat64(metrics.periodPending.WithLabelValues("crypto", "1m")))
	require.Zero(t, testutil.ToFloat64(metrics.periodPending.WithLabelValues("crypto", "5m")), "disappeared frequencies must reset instead of remaining stale")

	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() != "moox_collector_period_pending_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			labels := metricLabels(metric)
			require.Len(t, labels, 2)
			require.Contains(t, labels, "space_id")
			require.Contains(t, labels, "frequency")
		}
	}
}

func TestPeriodFailureMetricsUseOnlyBoundedLabels(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewMetrics(registry)
	metrics.ObservePeriodFailurePending("crypto", "1m", 1)
	metrics.ObservePeriodFailureReportRetry("crypto", "1m", "timeout")
	metrics.ObservePeriodFailureReportRetry("crypto", "1m", "canceled")
	metrics.ObservePeriodFailureReportRetry("crypto", "1m", "storage_error")
	metrics.ObservePeriodFailureReportRetry("crypto", "1m", "storage_client_error")
	metrics.ObservePeriodFailureMissedDeadline("crypto", "1m")
	// Adding or deleting task-generated Dataset targets leaves metric identity
	// unchanged because no dataset, subject, task, or retry key is a label.
	metrics.ObservePeriodFailurePending("crypto", "1m", 2)
	families, err := registry.Gather()
	require.NoError(t, err)
	pending := metricFamily(t, families, "moox_collector_period_failure_pending")
	require.Len(t, pending.GetMetric(), 1)
	require.Equal(t, map[string]string{"space_id": "crypto", "frequency": "1m"}, metricLabels(pending.GetMetric()[0]))
	require.Equal(t, float64(2), pending.GetMetric()[0].GetGauge().GetValue())
	retries := metricFamily(t, families, "moox_collector_period_failure_report_retries_total")
	require.Len(t, retries.GetMetric(), 4)
	gotOutcomes := make(map[string]float64, len(retries.GetMetric()))
	for _, metric := range retries.GetMetric() {
		labels := metricLabels(metric)
		require.Equal(t, "crypto", labels["space_id"])
		require.Equal(t, "1m", labels["frequency"])
		gotOutcomes[labels["outcome"]] = metric.GetCounter().GetValue()
	}
	require.Equal(t, map[string]float64{"timeout": 1, "canceled": 1, "storage_error": 1, "storage_client_error": 1}, gotOutcomes)
	missed := metricFamily(t, families, "moox_collector_period_failure_missed_deadline_total")
	require.Len(t, missed.GetMetric(), 1)
	require.Equal(t, map[string]string{"space_id": "crypto", "frequency": "1m"}, metricLabels(missed.GetMetric()[0]))
	require.Equal(t, float64(1), missed.GetMetric()[0].GetCounter().GetValue())

	metrics.ObservePeriodFailurePending("crypto", "2m", 1)
	metrics.ObservePeriodFailureReportRetry("crypto", "1m", "retry-key-secret")
	families, err = registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() == "moox_collector_period_failure_pending" {
			require.Len(t, family.GetMetric(), 1, "invalid frequency must not add a label combination")
		}
		if family.GetName() == "moox_collector_period_failure_report_retries_total" {
			require.Len(t, family.GetMetric(), 4, "unbounded outcome must not add a label combination")
		}
	}
}

func TestPeriodFailureReporterMetricsTrackDatasetTasksWithoutDatasetLabels(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	registry := prometheus.NewRegistry()
	reporter := NewPeriodFailureReporter(db.FetchRetries(), func(string, string, string) (Storage, error) { return nil, nil }, "storage", "crypto")
	reporter.SetMetrics(NewMetrics(registry))
	period := time.Now().UTC().Truncate(time.Minute)
	targetA, retryA := addMetricFailureTask(t, db, "task-a", "dataset-a", "target-a", period)
	require.NoError(t, reporter.refreshPendingMetrics(ctx, "crypto"))
	assertPendingFailureMetric(t, registry, 1)

	_, retryB := addMetricFailureTask(t, db, "task-b", "dataset-b", "target-b", period)
	require.NoError(t, reporter.refreshPendingMetrics(ctx, "crypto"))
	assertPendingFailureMetric(t, registry, 2)

	require.NoError(t, db.FetchRetries().ApplyPeriodFailureReportResults(ctx, "crypto", retryA.RetryKey, []domain.PeriodFailureTargetResult{{
		WriteTargetID: targetA.ID, SpaceID: "crypto", DatasetID: targetA.DatasetID, Frequency: "1m", PeriodTime: period,
		SeriesHash: targetA.SeriesHash, ExpectedCount: targetA.ExpectedCount, SeriesIndex: targetA.SeriesIndex, Disposition: "recorded", ObservedAt: period,
	}}, ""))
	require.NoError(t, db.DeleteTaskRuntime(ctx, "crypto", "task-a"))
	require.NoError(t, db.Tasks().DeleteByTaskID(ctx, "crypto", "task-a"))
	require.NoError(t, reporter.refreshPendingMetrics(ctx, "crypto"))
	assertPendingFailureMetric(t, registry, 1)
	stored, err := db.FetchRetries().Get(ctx, "crypto", retryB.RetryKey)
	require.NoError(t, err)
	require.Equal(t, domain.PeriodFailureReportPending, stored.PeriodFailureReportState)
}

func addMetricFailureTask(t *testing.T, db *store.Store, taskID, datasetID, targetID string, period time.Time) (domain.WriteTarget, domain.RetryItem) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, db.Tasks().Create(ctx, domain.CollectionTask{SpaceID: "crypto", TaskID: taskID, TaskName: taskID, DataType: "kline", Enabled: true}))
	instanceID := "instance-" + taskID
	require.NoError(t, db.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{{
		SpaceID: "crypto", InstanceID: instanceID, DataType: "kline",
		SubjectID: "BTC-USDT", Frequency: "1m", TaskParams: `{}`,
	}}))
	target := domain.WriteTarget{ID: targetID, SpaceID: "crypto", InstanceID: instanceID, TaskID: taskID, DatasetID: datasetID, SeriesIndex: 0, SeriesHash: "hash-" + taskID, ExpectedCount: 1, Status: "failed"}
	require.NoError(t, db.TaskInstances().UpsertWriteTargets(ctx, []domain.WriteTarget{target}))
	targetsJSON, err := json.Marshal([]domain.WriteTarget{target})
	require.NoError(t, err)
	item := domain.CollectionItem{InstanceID: instanceID, SubjectID: "BTC-USDT", DatasetID: datasetID, Frequency: "1m", MarketType: "spot", TargetDataTime: period.Format(time.RFC3339Nano)}
	itemJSON, err := json.Marshal(item)
	require.NoError(t, err)
	retry := domain.RetryItem{
		SpaceID: "crypto", RetryKey: "retry-" + taskID, InstanceID: instanceID, SubjectID: item.SubjectID, Frequency: "1m",
		TargetDataTime: period, TaskJSON: string(itemJSON), FailureTargetsJSON: string(targetsJSON), Status: "permanent_failed",
	}
	require.NoError(t, db.FetchRetries().Upsert(ctx, &retry))
	return target, retry
}

func assertPendingFailureMetric(t *testing.T, registry *prometheus.Registry, want float64) {
	t.Helper()
	families, err := registry.Gather()
	require.NoError(t, err)
	metric := metricFamily(t, families, "moox_collector_period_failure_pending")
	require.Len(t, metric.GetMetric(), 1, "adding/removing task-generated datasets must not add label combinations")
	require.Equal(t, map[string]string{"space_id": "crypto", "frequency": "1m"}, metricLabels(metric.GetMetric()[0]))
	require.Equal(t, want, metric.GetMetric()[0].GetGauge().GetValue())
}

func TestMetricsClearAssignmentPending(t *testing.T) {
	metrics := NewMetrics(prometheus.NewRegistry())
	metrics.ObserveAssignmentPending("crypto", true, time.Unix(1722652200, 0))
	metrics.ObserveAssignmentPending("crypto", false, time.Time{})
	require.Zero(t, testutil.ToFloat64(metrics.assignmentPending.WithLabelValues("crypto")))
	require.Zero(t, testutil.ToFloat64(metrics.assignmentPendingSince.WithLabelValues("crypto")))
}

func TestMetricsExposeTimerCapacityHeadroom(t *testing.T) {
	metrics := NewMetrics(prometheus.NewRegistry())
	metrics.ObserveTimerCapacity("crypto", 45, 52, 0)
	require.Equal(t, float64(45), testutil.ToFloat64(metrics.timerCapacityTotal.WithLabelValues("crypto")))
	require.Equal(t, float64(52), testutil.ToFloat64(metrics.timerCapacityRequired.WithLabelValues("crypto")))
	require.Equal(t, float64(0), testutil.ToFloat64(metrics.timerCapacityActive.WithLabelValues("crypto")))
	require.Equal(t, float64(-7), testutil.ToFloat64(metrics.timerCapacityHeadroom.WithLabelValues("crypto")))
}

func TestMetricsUseFixedErrorReasons(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewMetrics(registry)
	for _, reason := range []string{"capacity", "rules", "symbols", "dns", "cloudnode", "environment"} {
		metrics.ObserveAssignmentError("crypto", reason)
	}
	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() != "moox_collector_market_fetch_assignment_errors_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() != "reason" {
					continue
				}
				switch label.GetValue() {
				case "capacity", "rules", "symbols", "dns", "cloudnode", "environment":
				default:
					t.Fatalf("unexpected error reason %q", label.GetValue())
				}
			}
		}
	}
}

func TestMetricsRemoveDeletedAssignmentAndTimerLabels(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewMetrics(registry)
	metrics.ObserveAssignmentDesired("crypto", "1m", 1, 1)
	metrics.ObserveTimerState("crypto", "old-timer", "true", 1)
	metrics.ResetAssignmentScope("crypto")
	metrics.ResetTimerScope("crypto")
	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() == "moox_collector_market_fetch_assignment_required" || family.GetName() == "moox_collector_market_fetch_assignment_active" || family.GetName() == "moox_collector_market_fetch_timer_available" {
			if len(family.GetMetric()) > 0 {
				t.Fatalf("expected deleted gauge labels to be absent, family=%s", family.GetName())
			}
		}
	}
}

func TestAssignmentMetricsCountEnvironmentSplits(t *testing.T) {
	groups := []TaskGroup{
		{Provider: "binance", MarketType: "spot", DatasetID: "bars", Frequency: "1m", Subjects: []string{"BTC-USDT"}},
		{Provider: "binance", MarketType: "spot", DatasetID: "bars", Frequency: "1m", Subjects: []string{"ETH-USDT"}},
	}
	assignments := []NodeAssignment{
		{Provider: "binance", MarketType: "spot", DatasetID: "bars", Frequency: "1m", Subjects: []string{"BTC-USDT"}},
		{Provider: "binance", MarketType: "spot", DatasetID: "bars", Frequency: "1m", Subjects: []string{"ETH-USDT"}},
	}
	scopes := assignmentMetricScopes(groups, assignments)
	require.Len(t, scopes, 1)
	require.Equal(t, 2, scopes[0].Required)
	require.Equal(t, 2, scopes[0].Active)
}

func TestMetricsRecoveryCanSucceedWithoutAssignmentScopes(t *testing.T) {
	metrics := NewMetrics(prometheus.NewRegistry())
	metrics.ObserveAssignmentFailure("crypto", "rules")
	metrics.ObserveAssignmentSuccess("crypto", 1722772800)
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.assignmentHealthy.WithLabelValues("crypto")))
}

func TestMetricsKeepOnlyCurrentCoordinationFailureReason(t *testing.T) {
	metrics := NewMetrics(prometheus.NewRegistry())
	metrics.ObserveAssignmentFailure("crypto", "submit_timeout")
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.assignmentFailure.WithLabelValues("crypto", "submit_timeout")))

	metrics.ObserveAssignmentFailure("crypto", "cloudnode")
	require.Zero(t, testutil.ToFloat64(metrics.assignmentFailure.WithLabelValues("crypto", "submit_timeout")))
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.assignmentFailure.WithLabelValues("crypto", "cloudnode")))

	metrics.ObserveAssignmentSuccess("crypto", 1722772800)
	require.Zero(t, testutil.ToFloat64(metrics.assignmentFailure.WithLabelValues("crypto", "cloudnode")))
}

func TestMetricsResetRequirementsPreservesLastActiveAssignment(t *testing.T) {
	metrics := NewMetrics(prometheus.NewRegistry())
	metrics.ObserveAssignmentDesired("crypto", "1m", 34, 34)
	metrics.ResetAssignmentRequirements("crypto")
	metrics.ObserveAssignmentRequired("crypto", "1m", 34)

	require.Equal(t, float64(34), testutil.ToFloat64(metrics.assignmentRequired.WithLabelValues("crypto", "1m")))
	require.Equal(t, float64(34), testutil.ToFloat64(metrics.assignmentActive.WithLabelValues("crypto", "1m")))
}

func TestAssignmentMetricsAggregateAcrossDatasetsWithoutDatasetLabels(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewMetrics(registry)
	reconciler := &Reconciler{Metrics: metrics}
	groups := []TaskGroup{
		{Provider: "binance", MarketType: "spot", DatasetID: "bars-a", Frequency: "1m", Subjects: []string{"BTC-USDT"}},
		{Provider: "binance", MarketType: "spot", DatasetID: "bars-a", Frequency: "1m", Subjects: []string{"ETH-USDT"}},
		{Provider: "binance", MarketType: "spot", DatasetID: "bars-b", Frequency: "1m", Subjects: []string{"SOL-USDT"}},
	}
	assignments := []NodeAssignment{
		{Provider: "binance", MarketType: "spot", DatasetID: "bars-a", Frequency: "1m", Subjects: []string{"BTC-USDT"}},
		{Provider: "binance", MarketType: "spot", DatasetID: "bars-b", Frequency: "1m", Subjects: []string{"SOL-USDT"}},
	}

	reconciler.observeAssignmentRequirements("crypto", groups)
	reconciler.observeAssignmentDesiredMetrics("crypto", groups, assignments)
	reconciler.observeAssignmentMetrics("crypto", groups, assignments, 1722772800)

	families, err := registry.Gather()
	require.NoError(t, err)
	for _, name := range []string{
		"moox_collector_market_fetch_assignment_required",
		"moox_collector_market_fetch_assignment_active",
	} {
		family := metricFamily(t, families, name)
		require.Len(t, family.GetMetric(), 1)
		metric := family.GetMetric()[0]
		require.Equal(t, map[string]string{"space_id": "crypto", "frequency": "1m"}, metricLabels(metric))
		if name == "moox_collector_market_fetch_assignment_required" {
			require.Equal(t, float64(3), metric.GetGauge().GetValue())
		} else {
			require.Equal(t, float64(2), metric.GetGauge().GetValue())
		}
	}
}

func TestAssignmentMetricsBoundUnsupportedFrequencyLabels(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewMetrics(registry)
	metrics.ObserveAssignmentDesired("crypto", "task-specific-frequency", 2, 1)

	families, err := registry.Gather()
	require.NoError(t, err)
	family := metricFamily(t, families, "moox_collector_market_fetch_assignment_required")
	require.Len(t, family.GetMetric(), 1)
	require.Equal(t, map[string]string{"space_id": "crypto", "frequency": "unknown"}, metricLabels(family.GetMetric()[0]))
}

func TestMetricsExposeLowCardinalityFeedDimensions(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewMetrics(registry)

	metrics.ObserveFeedResult(FeedMetric{
		MarketID: "stockcn", RouteID: StockCNRouteID, ProviderID: "sina",
		FeedKind: "kline", GroupID: 7, GroupCount: 200, BatchKind: "realtime", Result: "success",
	})

	families, err := registry.Gather()
	require.NoError(t, err)
	family := metricFamily(t, families, "moox_collector_market_feed_results_total")
	require.Len(t, family.GetMetric(), 1)
	labels := metricLabels(family.GetMetric()[0])
	require.Equal(t, map[string]string{
		"market_id": "stockcn", "route_id": StockCNRouteID, "provider_id": "sina",
		"feed_kind": "kline", "group_id": "7", "batch_kind": "realtime", "result": "success",
	}, labels)
	for _, forbidden := range []string{"subject", "subject_id", "ip", "candidate_chain", "function_name"} {
		_, exists := labels[forbidden]
		require.False(t, exists, "high-cardinality label %q must not be exposed", forbidden)
	}
}

func TestMetricsAllowConfiguredCryptoKlineFrequencies(t *testing.T) {
	for _, route := range []string{"binance_spot_kline_1h", "binance_swap_kline_1w"} {
		marketID, bounded := boundedMarketRoute("crypto", route)
		require.Equal(t, "crypto", marketID)
		require.Equal(t, route, bounded)
	}
	_, bounded := boundedMarketRoute("crypto", "binance_spot_kline_2m")
	require.Equal(t, "unknown", bounded)
}

func TestMetricsRejectFeedGroupOutsideConfiguredRange(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewMetrics(registry)
	metrics.ObserveFeedResult(FeedMetric{MarketID: "stockcn", RouteID: StockCNRouteID, ProviderID: "sina", FeedKind: "kline", GroupID: 200, GroupCount: 200, BatchKind: "realtime", Result: "success"})
	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		require.NotEqual(t, "moox_collector_market_feed_results_total", family.GetName())
	}
}

func metricFamily(t *testing.T, families []*dto.MetricFamily, name string) *dto.MetricFamily {
	t.Helper()
	for _, family := range families {
		if family.GetName() == name {
			return family
		}
	}
	t.Fatalf("metric family %q not found", name)
	return nil
}

func metricLabels(metric *dto.Metric) map[string]string {
	labels := make(map[string]string, len(metric.GetLabel()))
	for _, label := range metric.GetLabel() {
		labels[label.GetName()] = label.GetValue()
	}
	return labels
}
