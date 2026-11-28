package report

import (
	"slices"
	"time"
)

// ModuleHealthCheck 描述一类用于评估模块健康的内置信号；数据集级新鲜度阈值由 Monitor 配置中的 RealtimeTimeSeriesPolicy 定义。
type ModuleHealthCheck struct {
	ID      string
	Module  string
	Enabled bool
	MaxLag  time.Duration
}

var builtInModuleHealthChecks = []ModuleHealthCheck{
	{ID: "strategy-targets", Module: "strategy", MaxLag: 10 * time.Minute, Enabled: true},
	{ID: "trade-rebalance", Module: "trade", MaxLag: 5 * time.Minute, Enabled: true},
	{ID: "monitor-metrics", Module: "monitor", MaxLag: 2 * time.Minute, Enabled: true},
}

// BuiltInModuleHealthChecks returns a copy of the finite module health-check
// registry. Dataset expectations remain dynamic and are not part of this list.
func BuiltInModuleHealthChecks() []ModuleHealthCheck {
	return slices.Clone(builtInModuleHealthChecks)
}

// HealthCheckIDsForModule returns enabled health-check IDs for one module.
func HealthCheckIDsForModule(module string) []string {
	var ids []string
	for _, check := range builtInModuleHealthChecks {
		if check.Enabled && check.Module == module {
			ids = append(ids, check.ID)
		}
	}
	return ids
}
