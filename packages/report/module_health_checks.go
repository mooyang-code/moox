package report

import (
	"slices"
	"time"
)

// ModuleHealthCheck describes one code-owned signal group used to assess a
// module. Dataset-specific freshness thresholds live in DatasetHealthPolicy.
type ModuleHealthCheck struct {
	ID                    string
	Module                string
	Enabled               bool
	MaxLag                time.Duration
	CheckFreshness        bool
	CheckWatermark        bool
	ObservabilityDeferred bool
}

var builtInModuleHealthChecks = []ModuleHealthCheck{
	{ID: "cloudnode-jobs", Module: "cloudnode", MaxLag: 5 * time.Minute, Enabled: true},
	{ID: "collector-market-data", Module: "collector", MaxLag: 5 * time.Minute, Enabled: true},
	{ID: "factor-calculation", Module: "factor", MaxLag: 10 * time.Minute, Enabled: true},
	{ID: "strategy-targets", Module: "strategy", MaxLag: 10 * time.Minute, Enabled: true},
	{ID: "trade-rebalance", Module: "trade", MaxLag: 5 * time.Minute, Enabled: true},
	{ID: "archive-materialize", Module: "archive", MaxLag: 15 * time.Minute, Enabled: true},
	{
		ID: "monitor-metrics", Module: "monitor", MaxLag: 2 * time.Minute, Enabled: true,
		CheckFreshness: true, CheckWatermark: true,
	},
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
