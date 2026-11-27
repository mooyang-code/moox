package doctor

import "time"

// ModuleIdleAfter 是最近一次失败之后多久没有新的运行就算模块闲置。
// 取 1 小时而不是 MaxLag：每小时跑一次、次次失败的模块要一直判失败，不能在两次运行之间恢复又失败。
const ModuleIdleAfter = time.Hour

// ModuleRunSignals 是模块运行健康检查的共享输入（Monitor 与 Doctor 共用同一套判定口径）。
type ModuleRunSignals struct {
	LastSuccess time.Time
	LastError   time.Time
	MaxLag      time.Duration
}

// ModuleRunReason 是模块运行健康判定的规范原因码。
const (
	ModuleRunNoRun             = "no_run"
	ModuleRunRecentSuccess     = "recent_success"
	ModuleRunIdleAfterError    = "idle_after_error"
	ModuleRunNeverSucceeded    = "never_succeeded"
	ModuleRunFailingOverMaxLag = "failing_over_max_lag"
)

// ModuleRunVerdict 是模块运行健康的判定结论。
type ModuleRunVerdict struct {
	Status CheckStatus
	Reason string
}

// EvaluateModuleRunHealth 是 Monitor 业务检查与 Doctor 诊断共用的模块运行健康判定口径：
//   - 从未上报过成功或失败：StatusSkipped（no_run）；
//   - 最近一次运行失败（LastError > LastSuccess）且距上次成功已超过 MaxLag：
//     若距最近一次失败也已超过 max(MaxLag, ModuleIdleAfter)，按闲置判 StatusPass（idle_after_error）；
//     否则判 StatusFail（never_succeeded 或 failing_over_max_lag）；
//   - 其余情形（最近一次成功，或失败后仍在 MaxLag 容忍期内）：StatusPass（recent_success）。
func EvaluateModuleRunHealth(signals ModuleRunSignals, now time.Time) ModuleRunVerdict {
	if signals.LastSuccess.IsZero() && signals.LastError.IsZero() {
		return ModuleRunVerdict{Status: StatusSkipped, Reason: ModuleRunNoRun}
	}
	failing := signals.LastError.After(signals.LastSuccess) && now.Sub(signals.LastSuccess) > signals.MaxLag
	if !failing {
		return ModuleRunVerdict{Status: StatusPass, Reason: ModuleRunRecentSuccess}
	}
	idleWindow := max(signals.MaxLag, ModuleIdleAfter)
	if now.Sub(signals.LastError) > idleWindow {
		return ModuleRunVerdict{Status: StatusPass, Reason: ModuleRunIdleAfterError}
	}
	if signals.LastSuccess.IsZero() {
		return ModuleRunVerdict{Status: StatusFail, Reason: ModuleRunNeverSucceeded}
	}
	return ModuleRunVerdict{Status: StatusFail, Reason: ModuleRunFailingOverMaxLag}
}

// ComponentModule 把组件 ID 映射到模块指标前缀（factor-mgr 的模块指标前缀是 factor）。
func ComponentModule(componentID string) string {
	if componentID == "factor-mgr" {
		return "factor"
	}
	return componentID
}

// ModuleComponent 把模块指标前缀映射回对应的组件 ID（factor 对应 factor-mgr）。
func ModuleComponent(module string) string {
	if module == "factor" {
		return "factor-mgr"
	}
	return module
}

// HealthStatusSeverity 是 Monitor 概览与 Doctor 告警清单共用的状态严重程度映射：
// healthy/ok/disabled/unchecked 返回空（非问题），down/stale/firing 返回 "critical"，其余异常状态返回 "warning"。
func HealthStatusSeverity(status string) string {
	switch status {
	case "", "healthy", "ok", "disabled", "unchecked":
		return ""
	case "down", "stale", "firing":
		return "critical"
	default:
		return "warning"
	}
}

// HealthStatusRank 返回规范健康状态的严重程度排序权重（越大越严重），供概览与诊断聚合共用。
func HealthStatusRank(status string) int {
	switch status {
	case "down":
		return 5
	case "degraded":
		return 4
	case "unknown":
		return 3
	case "healthy":
		return 2
	default:
		return 1
	}
}
