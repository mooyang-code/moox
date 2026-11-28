package checks

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/alerttext"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/packages/doctor"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/report"
)

// moduleHealthNames 是内置模块健康检查对应的中文展示名。
var moduleHealthNames = map[string]string{
	"strategy-targets": "策略目标生成",
	"trade-rebalance":  "交易调仓",
	"monitor-metrics":  "监控指标接收",
}

// moduleHealthItems 在模块上报过运行之后，按与 Doctor 共用的判定口径把每个内置模块健康检查转成一项业务检查。
func moduleHealthItems(ctx context.Context, query *monmetrics.QueryService, healthChecks []report.ModuleHealthCheck, now time.Time) ([]Fact, error) {
	if query == nil {
		return nil, nil
	}
	items := make([]Fact, 0, len(healthChecks))
	for _, check := range healthChecks {
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
		verdict := doctor.EvaluateModuleRunHealth(doctor.ModuleRunSignals{
			LastSuccess: lastSuccess,
			LastError:   lastError,
			MaxLag:      check.MaxLag,
		}, now)
		if verdict.Status == doctor.StatusSkipped {
			continue
		}
		name := moduleHealthNames[check.ID]
		if name == "" {
			name = check.ID
		}
		item := Fact{
			SpaceID: events.SystemSpaceID, CheckID: "module:" + check.ID, Name: name,
			Status: domain.CheckStatusOK, Reason: "最近一次运行成功",
		}
		switch verdict.Reason {
		case doctor.ModuleRunIdleAfterError:
			item.Reason = fmt.Sprintf("最近一次运行失败（%s）之后已 %s没有新的运行，按闲置处理",
				alerttext.Time(lastError), alerttext.Duration(now.Sub(lastError)))
		case doctor.ModuleRunNeverSucceeded:
			item.Status = domain.CheckStatusDown
			item.Reason = fmt.Sprintf("最近一次运行失败（%s），且从未成功过", alerttext.Time(lastError))
		case doctor.ModuleRunFailingOverMaxLag:
			item.Status = domain.CheckStatusDown
			item.Reason = fmt.Sprintf("最近一次运行失败（%s），已 %s没有成功运行（上次成功 %s）",
				alerttext.Time(lastError), alerttext.Duration(now.Sub(lastSuccess)), alerttext.Time(lastSuccess))
		}
		items = append(items, item)
	}
	return items, nil
}

// latestModuleTime 返回指定模块健康检查在各上报实例中的最新时间戳。
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
