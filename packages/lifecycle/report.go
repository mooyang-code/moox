package lifecycle

import "github.com/mooyang-code/moox/packages/metricspb"

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
		out.PauseKind, out.PauseReason, out.PausedBy, out.PausedSince = string(s.Pause.Kind), s.Pause.Reason, s.Pause.By, toMillis(s.Pause.Since)
	}
	if s.Catchup != nil {
		out.Catchup = s.Catchup.Progress()
	}
	return out
}
