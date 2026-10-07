package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/packages/report"
)

// moduleHealthNames names each built-in module health check for people.
var moduleHealthNames = map[string]string{
	"cloudnode-jobs":        "云节点任务执行",
	"collector-market-data": "行情采集",
	"factor-calculation":    "因子计算",
	"strategy-targets":      "策略目标生成",
	"trade-rebalance":       "交易调仓",
	"archive-materialize":   "数据归档",
	"monitor-metrics":       "监控指标接收",
}

// moduleHealthItems turns each built-in module health check into a business
// check once its module has reported a run. The check fails while the latest
// run failed and no run has succeeded within the check's MaxLag; a module that
// is idle, or that recovers, is healthy.
func moduleHealthItems(ctx context.Context, query *monmetrics.QueryService, checks []report.ModuleHealthCheck, now time.Time) ([]businessFreshnessItem, error) {
	if query == nil {
		return nil, nil
	}
	items := make([]businessFreshnessItem, 0, len(checks))
	for _, check := range checks {
		if !check.Enabled {
			continue
		}
		lastSuccess, err := latestModuleTime(ctx, query, report.ModuleMetricName(check.Module, report.ModuleMetricLastSuccess), check.ID)
		if err != nil {
			return nil, err
		}
		lastError, err := latestModuleTime(ctx, query, report.ModuleMetricName(check.Module, report.ModuleMetricLastError), check.ID)
		if err != nil {
			return nil, err
		}
		if lastSuccess.IsZero() && lastError.IsZero() {
			continue
		}
		name := moduleHealthNames[check.ID]
		if name == "" {
			name = check.ID
		}
		item := businessFreshnessItem{
			spaceID: monmetrics.InternalMetricSpaceID, checkID: "module:" + check.ID, name: name,
			success: true, reason: "最近一次运行成功",
		}
		if lastError.After(lastSuccess) && now.Sub(lastSuccess) > check.MaxLag {
			item.success = false
			if lastSuccess.IsZero() {
				item.reason = fmt.Sprintf("最近一次运行失败（%s），且从未成功过", formatAlertTime(lastError))
			} else {
				item.reason = fmt.Sprintf("最近一次运行失败（%s），已 %s没有成功运行（上次成功 %s）",
					formatAlertTime(lastError), formatAlertDuration(now.Sub(lastSuccess)), formatAlertTime(lastSuccess))
			}
		}
		items = append(items, item)
	}
	return items, nil
}

// latestModuleTime is the newest timestamp a module metric reports for one
// health check across its reporter instances.
func latestModuleTime(ctx context.Context, query *monmetrics.QueryService, metricName, healthCheck string) (time.Time, error) {
	series, _, err := query.Catalog().ListSeries(ctx, "", metricName, "", 0, 100)
	if err != nil {
		return time.Time{}, err
	}
	var latest time.Time
	for _, item := range series {
		var labels map[string]string
		if json.Unmarshal([]byte(item.LabelsJSON), &labels) != nil || labels["health_check"] != healthCheck {
			continue
		}
		value, err := query.Latest(ctx, item.SeriesID)
		if err != nil || value.Value <= 0 {
			continue
		}
		if at := time.Unix(int64(value.Value), 0).UTC(); at.After(latest) {
			latest = at
		}
	}
	return latest, nil
}
