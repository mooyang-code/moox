package bootstrap

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestModuleHealthItemsFlagFailingModulesOnly(t *testing.T) {
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Close() })
	require.NoError(t, manager.ApplySchema(schema.SQL()))
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	type sample struct {
		module, metric, check string
		at                    time.Time
	}
	samples := []sample{
		// strategy failed ten minutes ago and last succeeded an hour ago.
		{"strategy", report.ModuleMetricLastSuccess, "strategy-targets", now.Add(-time.Hour)},
		{"strategy", report.ModuleMetricLastError, "strategy-targets", now.Add(-10 * time.Minute)},
		// trade failed once but succeeded since.
		{"trade", report.ModuleMetricLastError, "trade-rebalance", now.Add(-10 * time.Minute)},
		{"trade", report.ModuleMetricLastSuccess, "trade-rebalance", now.Add(-time.Minute)},
		// factor's latest run failed, but within its MaxLag of a success.
		{"factor", report.ModuleMetricLastSuccess, "test-within-lag", now.Add(-3 * time.Minute)},
		{"factor", report.ModuleMetricLastError, "test-within-lag", now.Add(-time.Minute)},
		// collector 最近一次失败在 2 小时前，之后再没有运行（闲置），判健康。
		{"collector", report.ModuleMetricLastSuccess, "test-idle", now.Add(-3 * time.Hour)},
		{"collector", report.ModuleMetricLastError, "test-idle", now.Add(-2 * time.Hour)},
		// monitor 从没成功过、50 分钟前失败：还在闲置判定窗口内，仍判失败（每小时跑一次的模块不能在两次运行之间恢复）。
		{"monitor", report.ModuleMetricLastError, "monitor-metrics", now.Add(-50 * time.Minute)},
	}
	query, err := store.WithDatabase(manager, func(db *gorm.DB) *monmetrics.QueryService {
		for index, item := range samples {
			seriesID := fmt.Sprintf("series-%d", index)
			labels := fmt.Sprintf(`{"health_check":%q,"stage":"evaluate"}`, item.check)
			name := report.ModuleMetricName(item.module, item.metric)
			require.NoError(t, db.Create(&monmetrics.MetricSeries{ServiceName: "moox_" + item.module, InstanceID: "i", SeriesID: seriesID, MetricName: name, MetricType: "gauge", LabelsJSON: labels, LastSeenAt: now}).Error)
			require.NoError(t, db.Create(&monmetrics.MetricLatest{SeriesID: seriesID, ServiceName: "moox_" + item.module, InstanceID: "i", MetricName: name, MetricType: "gauge", LabelsJSON: labels, Value: float64(item.at.Unix()), ObservedAt: now}).Error)
		}
		return monmetrics.NewQueryService(monmetrics.NewMetricMessageStore(db), nil)
	})
	require.NoError(t, err)

	// 内置清单之外再加两项只在测试里用的检查，覆盖“失败但 MaxLag 内成功过”与“闲置”两种判定。
	checks := append(report.BuiltInModuleHealthChecks(),
		report.ModuleHealthCheck{ID: "test-within-lag", Module: "factor", MaxLag: 10 * time.Minute, Enabled: true},
		report.ModuleHealthCheck{ID: "test-idle", Module: "collector", MaxLag: 5 * time.Minute, Enabled: true})
	items, err := moduleHealthItems(context.Background(), query, checks, now)
	require.NoError(t, err)
	byID := map[string]businessFreshnessItem{}
	for _, item := range items {
		byID[item.checkID] = item
	}
	require.Len(t, byID, 5, "modules that never reported a run get no check")
	require.False(t, byID["module:strategy-targets"].success)
	require.Equal(t, "策略目标生成", byID["module:strategy-targets"].name)
	require.Contains(t, byID["module:strategy-targets"].reason, "已 1 小时没有成功运行")
	require.True(t, byID["module:trade-rebalance"].success)
	require.True(t, byID["module:test-within-lag"].success)
	require.True(t, byID["module:test-idle"].success)
	require.Contains(t, byID["module:test-idle"].reason, "之后已 2 小时没有新的运行，按闲置处理")
	require.False(t, byID["module:monitor-metrics"].success)
	require.Contains(t, byID["module:monitor-metrics"].reason, "且从未成功过")
}

func TestKlineReasonTextForViewBehindCompletedPeriod(t *testing.T) {
	require.Contains(t, klineReasonText(monmetrics.KlineFreshnessReport{Reason: "calendar_not_covered"}, time.Now()), "日历未覆盖")
	completed := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	report := monmetrics.KlineFreshnessReport{Reason: "view_behind_latest_completed_period", StaleAge: time.Hour,
		Rule: monmetrics.KlineFreshnessRule{LatestCompletedPeriod: completed}}
	require.Equal(t, "采集已完成到 10-07 20:00 这一期，但还没有看到该结果的数据（这一根已过收线 1 小时）", klineReasonText(report, completed.Add(time.Hour)))
	report.OldestDataTime = completed.Add(-time.Hour)
	require.Equal(t, "采集已完成到 10-07 20:00 这一期，但结果只更新到 10-07 19:00，之后的一根已过收线 1 小时", klineReasonText(report, completed.Add(time.Hour)))
}
