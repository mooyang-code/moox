package lifecycle

import (
	"time"

	"github.com/mooyang-code/moox/packages/metricspb"
)

// ReportState 是随指标上报带给 Monitor 的模块级生命周期（packages/report 的 SetLifecycle 用它）；首页的系统状态读它。
func (c *Controller) ReportState() *metricspb.ModuleLifecycle {
	if c == nil {
		return nil
	}
	return c.State().Report()
}

// Report 把状态快照转成上报里的生命周期。
func (s State) Report() *metricspb.ModuleLifecycle {
	out := &metricspb.ModuleLifecycle{Stage: string(s.Stage), Reason: s.Reason}
	if s.Pause != nil {
		out.PauseKind, out.PauseReason, out.PausedBy, out.PausedSince = string(s.Pause.Kind), s.Pause.Reason, s.Pause.By, rfc3339(s.Pause.Since)
	}
	if s.Catchup != nil {
		out.Catchup = s.Catchup.Report()
	}
	return out
}

// Report 把恢复追赶进度转成上报里的格式。
func (c Catchup) Report() *metricspb.CatchupProgress {
	return &metricspb.CatchupProgress{
		Since: rfc3339(c.Since), Target: rfc3339(c.Target), Done: c.Done, Total: c.Total, CaughtUp: c.CaughtUp,
		BlockedBy: c.BlockedBy, Unfillable: int32(c.Unfillable), Message: c.Message,
	}
}

func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
