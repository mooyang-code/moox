package marketfetch

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

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

func (f *operationalStatsFake) OperationalStats(context.Context, string, int) (store.OperationalStats, error) {
	return f.stats, f.err
}

func TestOperationalMetricsZeroResetBoundedLabelsAndFailedRefresh(t *testing.T) {
	registry := prometheus.NewRegistry()
	m := NewMetrics(registry)
	now := time.Now().UTC().Truncate(time.Second)
	f := &operationalStatsFake{stats: store.OperationalStats{DBBytes: 100, WALBytes: 20, Rows: map[string]store.OperationalRowCount{"instances": {Count: 9, Capped: true}, "task-secret": {Count: 1}}}}
	require.NoError(t, m.RefreshOperationalStats(context.Background(), f, "crypto", now))
	require.Equal(t, 9.0, testutil.ToFloat64(m.storeRows.WithLabelValues("crypto", "instances")))
	require.Equal(t, 1.0, testutil.ToFloat64(m.storeRowsCapped.WithLabelValues("crypto", "instances")))
	f.err = errors.New("task-secret query failure")
	require.Error(t, m.RefreshOperationalStats(context.Background(), f, "crypto", now.Add(time.Hour)))
	require.Equal(t, 9.0, testutil.ToFloat64(m.storeRows.WithLabelValues("crypto", "instances")))
	require.Equal(t, 0.0, testutil.ToFloat64(m.storeStatsHealthy.WithLabelValues("crypto")))
	require.Equal(t, float64(now.Unix()), testutil.ToFloat64(m.storeStatsLastSuccess.WithLabelValues("crypto")))
	f.err, f.stats = nil, store.OperationalStats{}
	require.NoError(t, m.RefreshOperationalStats(context.Background(), f, "crypto", now.Add(time.Hour)))
	require.Zero(t, testutil.ToFloat64(m.storeRows.WithLabelValues("crypto", "instances")))
	m.ObserveMaintenanceDeletes("crypto", map[string]int64{"period_readiness": 4, "task-secret": 3})
	require.Equal(t, 4.0, testutil.ToFloat64(m.maintenanceDeleted.WithLabelValues("crypto", "period_readiness")))
	m.ObserveMaintenanceDeletes("crypto", nil)
	require.Zero(t, testutil.ToFloat64(m.maintenanceDeleted.WithLabelValues("crypto", "period_readiness")))
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

func TestMetricsClearAssignmentPending(t *testing.T) {
	metrics := NewMetrics(prometheus.NewRegistry())
	metrics.ObserveAssignmentPending("crypto", true, time.Unix(1722652200, 0))
	metrics.ObserveAssignmentPending("crypto", false, time.Time{})
	require.Zero(t, testutil.ToFloat64(metrics.assignmentPending.WithLabelValues("crypto")))
	require.Zero(t, testutil.ToFloat64(metrics.assignmentPendingSince.WithLabelValues("crypto")))
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

func TestMetricsRecoveryCanSucceedWithoutAssignmentScopes(t *testing.T) {
	metrics := NewMetrics(prometheus.NewRegistry())
	metrics.ObserveAssignmentFailure("crypto", "rules")
	metrics.ObserveAssignmentSuccess("crypto", 1722772800)
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.assignmentHealthy.WithLabelValues("crypto")))
}

func TestMetricsKeepOnlyCurrentCoordinationFailureReason(t *testing.T) {
	metrics := NewMetrics(prometheus.NewRegistry())
	metrics.ObserveAssignmentFailure("crypto", ReasonPublishLeaseHeld)
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.assignmentFailure.WithLabelValues("crypto", ReasonPublishLeaseHeld)))

	metrics.ObserveAssignmentFailure("crypto", "cloudnode")
	require.Zero(t, testutil.ToFloat64(metrics.assignmentFailure.WithLabelValues("crypto", ReasonPublishLeaseHeld)))
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.assignmentFailure.WithLabelValues("crypto", "cloudnode")))

	metrics.ObserveAssignmentSuccess("crypto", 1722772800)
	require.Zero(t, testutil.ToFloat64(metrics.assignmentFailure.WithLabelValues("crypto", "cloudnode")))
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

func TestMetricsFailureReasonWhitelist(t *testing.T) {
	require.Equal(t, ReasonPublishLeaseHeld, normalizeAssignmentFailureReason(ReasonPublishLeaseHeld))
	// 白名单之外（包括已删除的 submit_timeout 与随手写的 publish_lease）一律归为 cloudnode。
	require.Equal(t, "cloudnode", normalizeAssignmentFailureReason("submit_timeout"))
	require.Equal(t, "cloudnode", normalizeAssignmentFailureReason("publish_lease"))
	require.Equal(t, "rules", normalizeAssignmentFailureReason(" rules "))
}

func TestMetricsFailureSinceTracksTheFirstAppearanceOfTheCurrentReason(t *testing.T) {
	metrics := NewMetrics(prometheus.NewRegistry())
	clock := time.Unix(1_700_000_000, 0)
	metrics.now = func() time.Time { return clock }
	since := func() float64 { return testutil.ToFloat64(metrics.assignmentFailureSince.WithLabelValues("crypto")) }
	require.Zero(t, since())

	metrics.ObserveAssignmentFailure("crypto", ReasonPublishLeaseHeld)
	require.Equal(t, float64(1_700_000_000), since())
	// 同一原因连续出现：起始时间保持不变。
	clock = clock.Add(3 * time.Minute)
	metrics.ObserveAssignmentFailure("crypto", ReasonPublishLeaseHeld)
	require.Equal(t, float64(1_700_000_000), since())
	// 原因变化：重新计时。
	metrics.ObserveAssignmentFailure("crypto", "cloudnode")
	require.Equal(t, float64(1_700_000_180), since())
	// 其他空间互不影响。
	require.Zero(t, testutil.ToFloat64(metrics.assignmentFailureSince.WithLabelValues("stockcn")))

	// 成功清零，再失败从新时间开始。
	metrics.ObserveAssignmentSuccess("crypto", 1_700_000_200)
	require.Zero(t, since())
	clock = clock.Add(time.Minute)
	metrics.ObserveAssignmentFailure("crypto", ReasonPublishLeaseHeld)
	require.Equal(t, float64(1_700_000_240), since())
}

func TestMetricsRoundOKRestoresHealthAndClearsFailureWithoutTouchingLastSuccess(t *testing.T) {
	metrics := NewMetrics(prometheus.NewRegistry())
	metrics.ObserveAssignmentSuccess("crypto", 1_700_000_000)
	metrics.ObserveAssignmentFailure("crypto", ReasonPublishLeaseHeld)
	require.Zero(t, testutil.ToFloat64(metrics.assignmentHealthy.WithLabelValues("crypto")))

	metrics.ObserveAssignmentRoundOK("crypto")
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.assignmentHealthy.WithLabelValues("crypto")))
	require.Zero(t, testutil.ToFloat64(metrics.assignmentFailure.WithLabelValues("crypto", ReasonPublishLeaseHeld)))
	require.Zero(t, testutil.ToFloat64(metrics.assignmentFailureSince.WithLabelValues("crypto")))
	require.Equal(t, float64(1_700_000_000), testutil.ToFloat64(metrics.assignmentLastSuccess.WithLabelValues("crypto")), "计划仍在进行时不刷新最近一次对账成功时间")
	require.NotPanics(t, func() { (*Metrics)(nil).ObserveAssignmentRoundOK("crypto") })
}

// fail 调用点用到的每个原因都必须在白名单里：白名单之外的原因会被悄悄归为 cloudnode，
// Monitor 就会把 Collector 本地数据库、Storage 等故障显示成“CloudNode 服务调用失败”。
func TestEveryFailureReasonUsedByTheReconcilerIsWhitelisted(t *testing.T) {
	allowed := map[string]bool{}
	for _, reason := range assignmentFailureReasons {
		allowed[reason] = true
	}
	used := map[string]int{}
	for _, name := range []string{"plan_reconciler.go", "plan_manager.go", "plan_aux.go"} {
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		require.NoError(t, err)
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "fail" || len(call.Args) == 0 {
				return true
			}
			literal, ok := call.Args[0].(*ast.BasicLit)
			if !ok {
				return true
			}
			reason, err := strconv.Unquote(literal.Value)
			require.NoError(t, err)
			used[reason]++
			require.Truef(t, allowed[reason], "%s: fail(%q, …) 的原因不在 assignmentFailureReasons 白名单里，会被归为 cloudnode", name, reason)
			return true
		})
	}
	require.GreaterOrEqual(t, len(used), 8, "扫描到的原因数量异常，扫描规则可能失效")
	// 白名单里没有任何调用点使用的原因是死条目。
	for _, reason := range assignmentFailureReasons {
		if reason == ReasonPublishLeaseHeld {
			continue // 由 failCloud 按错误类别选择，没有字面量调用点
		}
		require.Positivef(t, used[reason], "白名单里的 %q 没有任何 fail 调用点使用", reason)
	}
}

// 每个白名单原因都要在 Monitor 里有专属告警文案，否则 Monitor 只能显示通用提示。
func TestEveryFailureReasonHasAMonitorText(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "monitor", "internal", "observability", "overview.go"))
	require.NoError(t, err)
	source := string(raw)
	for _, reason := range assignmentFailureReasons {
		if reason == ReasonPublishLeaseHeld {
			require.Contains(t, source, "publishLeaseHeldReason = \""+reason+"\"")
			continue
		}
		require.Containsf(t, source, "{\""+reason+"\", \"", "Monitor 的 coordinationFailureTexts 缺少原因 %q 的告警文案", reason)
	}
}
