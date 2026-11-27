package fetchplan

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
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
	m := NewControlMetrics(registry)
	now := time.Now().UTC().Truncate(time.Second)
	f := &operationalStatsFake{stats: store.OperationalStats{DBBytes: 100, WALBytes: 20, Rows: map[string]store.OperationalRowCount{"resample_jobs": {Count: 9, Capped: true}, "task-secret": {Count: 1}}}}
	require.NoError(t, m.RefreshOperationalStats(context.Background(), f, "crypto", now))
	require.Equal(t, 9.0, testutil.ToFloat64(m.storeRows.WithLabelValues("crypto", "resample_jobs")))
	require.Equal(t, 1.0, testutil.ToFloat64(m.storeRowsCapped.WithLabelValues("crypto", "resample_jobs")))
	f.err = errors.New("task-secret query failure")
	require.Error(t, m.RefreshOperationalStats(context.Background(), f, "crypto", now.Add(time.Hour)))
	require.Equal(t, 9.0, testutil.ToFloat64(m.storeRows.WithLabelValues("crypto", "resample_jobs")))
	require.Equal(t, 0.0, testutil.ToFloat64(m.storeStatsHealthy.WithLabelValues("crypto")))
	require.Equal(t, float64(now.Unix()), testutil.ToFloat64(m.storeStatsLastSuccess.WithLabelValues("crypto")))
	f.err, f.stats = nil, store.OperationalStats{}
	require.NoError(t, m.RefreshOperationalStats(context.Background(), f, "crypto", now.Add(time.Hour)))
	require.Zero(t, testutil.ToFloat64(m.storeRows.WithLabelValues("crypto", "resample_jobs")))
	m.ObserveMaintenanceDeletes("crypto", map[string]int64{"backfill_notices": 4, "task-secret": 3})
	require.Equal(t, 4.0, testutil.ToFloat64(m.maintenanceDeleted.WithLabelValues("crypto", "backfill_notices")))
	m.ObserveMaintenanceDeletes("crypto", nil)
	require.Zero(t, testutil.ToFloat64(m.maintenanceDeleted.WithLabelValues("crypto", "backfill_notices")))
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

// coordination 返回本空间当前的协调关注项（采集时按 now 现算）。
func coordination(metrics *ControlMetrics, now time.Time) string {
	items := metrics.CoordinationAttention("k", 15*time.Minute, 5*time.Minute)(now)["k"]
	if len(items) == 0 {
		return ""
	}
	return items[0]
}

// 进行中且不满 5 分钟不报；超过宽限、或进行中结束后仍失败照常报；对账成功超过 15 分钟写明最后成功的时刻。
func TestCoordinationAttention(t *testing.T) {
	metrics := NewControlMetrics(prometheus.NewRegistry())
	now := time.Unix(1_700_000_000, 0).UTC()
	metrics.ObserveAssignmentSuccess("crypto", now.Unix())
	require.Empty(t, coordination(metrics, now.Add(time.Minute)))
	require.Contains(t, coordination(metrics, now.Add(16*time.Minute)), "最后对账成功 11-14 22:13 UTC")

	metrics.ObserveAssignmentFailure("crypto", "quota")
	metrics.ObserveAssignmentPending("crypto", true, now)
	require.Empty(t, coordination(metrics, now.Add(4*time.Minute)), "协调进行中、不满宽限不报")
	require.Contains(t, coordination(metrics, now.Add(6*time.Minute)), "配额超限")
	metrics.ObserveAssignmentPending("crypto", false, time.Time{})
	require.Contains(t, coordination(metrics, now.Add(time.Minute)), "crypto：函数个数")
}

func TestMetricsUseFixedErrorReasons(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewControlMetrics(registry)
	for _, reason := range []string{"capacity", "rules", "symbols", "dns", "cloud", "environment"} {
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
				case "capacity", "rules", "symbols", "dns", "cloud", "environment":
				default:
					t.Fatalf("unexpected error reason %q", label.GetValue())
				}
			}
		}
	}
}

func TestMetricsKeepOnlyCurrentCoordinationFailureReason(t *testing.T) {
	metrics := NewControlMetrics(prometheus.NewRegistry())
	now := time.Unix(1722772800, 0).UTC()
	metrics.ObserveAssignmentFailure("crypto", "quota")
	require.Contains(t, coordination(metrics, now), "配额超限")
	metrics.ObserveAssignmentFailure("crypto", "cloud")
	require.Contains(t, coordination(metrics, now), "调用腾讯云失败")
	require.NotContains(t, coordination(metrics, now), "配额超限")
	metrics.ObserveAssignmentSuccess("crypto", now.Unix())
	require.Empty(t, coordination(metrics, now))
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
	require.Equal(t, "quota", normalizeAssignmentFailureReason("quota"))
	// 白名单之外（包括已删除的原因、旧名 cloudfunction）一律归为 cloud。
	require.Equal(t, "cloud", normalizeAssignmentFailureReason("something_removed"))
	require.Equal(t, "cloud", normalizeAssignmentFailureReason("cloudfunction"))
	require.Equal(t, "rules", normalizeAssignmentFailureReason(" rules "))
}

func TestMetricsRoundOKRestoresHealthAndClearsFailureWithoutTouchingLastSuccess(t *testing.T) {
	metrics := NewControlMetrics(prometheus.NewRegistry())
	metrics.ObserveAssignmentSuccess("crypto", 1_700_000_000)
	metrics.ObserveAssignmentFailure("crypto", "quota")
	success := time.Unix(1_700_000_000, 0).UTC()
	require.Contains(t, coordination(metrics, success), "配额超限")

	metrics.ObserveAssignmentRoundOK("crypto")
	require.Empty(t, coordination(metrics, success))
	require.Contains(t, coordination(metrics, success.Add(16*time.Minute)), "最后对账成功", "恢复健康本身不刷新最近一次对账成功时间")
	require.NotPanics(t, func() { (*ControlMetrics)(nil).ObserveAssignmentRoundOK("crypto") })
}

// fail 调用点用到的每个原因都必须在白名单里：白名单之外的原因会被悄悄归为 cloud，
// Monitor 就会把 Collector 本地数据库、Storage 等故障显示成“调用腾讯云失败”。
func TestEveryFailureReasonUsedByTheReconcilerIsWhitelisted(t *testing.T) {
	allowed := map[string]bool{}
	for _, reason := range assignmentFailureReasons {
		allowed[reason] = true
	}
	used := map[string]int{}
	for _, name := range []string{
		"plan_reconciler.go", "plan_change.go", "plan_advance.go", "plan_fleet_verify.go", "plan_backfill_func.go",
		"plan_manager.go", "plan_cloud.go", "plan_package.go",
	} {
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
			require.Truef(t, allowed[reason], "%s: fail(%q, …) 的原因不在 assignmentFailureReasons 白名单里，会被归为 cloud", name, reason)
			return true
		})
	}
	require.GreaterOrEqual(t, len(used), 8, "扫描到的原因数量异常，扫描规则可能失效")
	// 白名单里没有任何调用点使用的原因是死条目。
	for _, reason := range assignmentFailureReasons {
		require.Positivef(t, used[reason], "白名单里的 %q 没有任何 fail 调用点使用", reason)
	}
}

// 每个白名单原因都要有专属告警文案（coordinationFailureTexts），否则关注项只能显示通用提示。
func TestEveryFailureReasonHasAText(t *testing.T) {
	for _, reason := range assignmentFailureReasons {
		require.NotEmptyf(t, coordinationFailureTexts[reason], "coordinationFailureTexts 缺少原因 %q 的告警文案", reason)
	}
}
