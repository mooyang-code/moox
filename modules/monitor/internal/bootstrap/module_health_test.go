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
		{"factor", report.ModuleMetricLastSuccess, "factor-calculation", now.Add(-3 * time.Minute)},
		{"factor", report.ModuleMetricLastError, "factor-calculation", now.Add(-time.Minute)},
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

	items, err := moduleHealthItems(context.Background(), query, report.BuiltInModuleHealthChecks(), now)
	require.NoError(t, err)
	byID := map[string]businessFreshnessItem{}
	for _, item := range items {
		byID[item.checkID] = item
	}
	require.Len(t, byID, 3, "modules that never reported a run get no check")
	require.False(t, byID["module:strategy-targets"].success)
	require.Equal(t, "策略目标生成", byID["module:strategy-targets"].name)
	require.Contains(t, byID["module:strategy-targets"].reason, "已 1 小时没有成功运行")
	require.True(t, byID["module:trade-rebalance"].success)
	require.True(t, byID["module:factor-calculation"].success)
}

func TestKlineReasonTextForViewBehindCompletedPeriod(t *testing.T) {
	completed := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	report := monmetrics.KlineFreshnessReport{Reason: "view_behind_latest_completed_period", StaleAge: time.Hour,
		Rule: monmetrics.KlineFreshnessRule{LatestCompletedPeriod: completed}}
	require.Equal(t, "采集已完成到 10-07 20:00 这一期，但还没有看到该结果的数据（已等待 1 小时）", klineReasonText(report, completed.Add(time.Hour)))
	report.OldestDataTime = completed.Add(-time.Hour)
	require.Equal(t, "采集已完成到 10-07 20:00 这一期，但结果只更新到 10-07 19:00，落后 1 小时", klineReasonText(report, completed.Add(time.Hour)))
}
