package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/alerttext"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/report"
)

// moduleHealthNames names each built-in module health check for people.
var moduleHealthNames = map[string]string{
	"cloudfunction-jobs":    "云函数实例任务执行",
	"collector-market-data": "行情采集",
	"factor-calculation":    "因子计算",
	"strategy-targets":      "策略目标生成",
	"trade-rebalance":       "交易调仓",
	"monitor-metrics":       "监控指标接收",
}

// moduleIdleAfter 是最近一次失败之后多久没有新的运行就算模块闲下来了。取 1 小时而不是 MaxLag：
// 每小时跑一次、次次失败的模块要一直判失败，不能在两次运行之间恢复又失败。
const moduleIdleAfter = time.Hour

// moduleHealthItems 在模块上报过运行之后，把每个内置模块健康检查转成一项业务检查。
// 最近一次运行失败、且 MaxLag 内没有成功过时判失败；模块恢复成功，或最近一次失败之后
// 超过 moduleIdleAfter（和 MaxLag 取大）没有新的运行（闲置，例如组合解绑后没有调仓可做），判健康。
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
			spaceID: events.SystemSpaceID, checkID: "module:" + check.ID, name: name,
			success: true, reason: "最近一次运行成功",
		}
		failing := lastError.After(lastSuccess) && now.Sub(lastSuccess) > check.MaxLag
		switch {
		case failing && now.Sub(lastError) > max(check.MaxLag, moduleIdleAfter):
			item.reason = fmt.Sprintf("最近一次运行失败（%s）之后已 %s没有新的运行，按闲置处理",
				alerttext.Time(lastError), alerttext.Duration(now.Sub(lastError)))
		case failing:
			item.success = false
			if lastSuccess.IsZero() {
				item.reason = fmt.Sprintf("最近一次运行失败（%s），且从未成功过", alerttext.Time(lastError))
			} else {
				item.reason = fmt.Sprintf("最近一次运行失败（%s），已 %s没有成功运行（上次成功 %s）",
					alerttext.Time(lastError), alerttext.Duration(now.Sub(lastSuccess)), alerttext.Time(lastSuccess))
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
