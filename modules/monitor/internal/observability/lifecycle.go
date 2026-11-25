package observability

import (
	"time"

	"github.com/mooyang-code/moox/packages/metricspb"
)

// LifecycleStatus 是模块随指标上报带来的模块级生命周期状态（设计 3.8），首页的系统状态由它汇总。
type LifecycleStatus struct {
	// Stage 是 running | paused | resuming。
	Stage   string
	Reason  string
	Pause   *LifecyclePause
	Catchup *LifecycleCatchup
}

// LifecyclePause 是暂停详情：类型（manual | system）、原因、操作者、开始时间。
type LifecyclePause struct {
	Kind, Reason, By, Since string
}

// LifecycleCatchup 是恢复追赶进度。
type LifecycleCatchup struct {
	Since      string
	Done       int64
	Total      int64
	CaughtUp   bool
	BlockedBy  string
	Unfillable int
	Message    string
}

// lifecycleFromReport 把上报里的生命周期转成概览用的结构；上报里没有时返回 nil。
func lifecycleFromReport(report *metricspb.ModuleLifecycle) *LifecycleStatus {
	if report == nil || report.GetStage() == "" {
		return nil
	}
	out := &LifecycleStatus{Stage: report.GetStage(), Reason: report.GetReason()}
	if report.GetPauseKind() != "" || report.GetPausedSince() != 0 {
		out.Pause = &LifecyclePause{Kind: report.GetPauseKind(), Reason: report.GetPauseReason(), By: report.GetPausedBy(), Since: millisText(report.GetPausedSince())}
	}
	if c := report.GetCatchup(); c != nil {
		out.Catchup = &LifecycleCatchup{
			Since: millisText(c.GetSince()), Done: c.GetDone(), Total: c.GetTotal(), CaughtUp: c.GetCaughtUp(),
			BlockedBy: c.GetBlockedBy(), Unfillable: int(c.GetUnfillable()), Message: c.GetMessage(),
		}
	}
	return out
}

// millisText 把上报里的毫秒时刻转成 Monitor 接口现用的 RFC3339 文本（Monitor 接口的时刻字段由 3H R4 组统一改毫秒）；0 是空串。
func millisText(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}
