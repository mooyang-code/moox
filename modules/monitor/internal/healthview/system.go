package healthview

import (
	"fmt"
	"sort"

	"github.com/mooyang-code/moox/modules/monitor/internal/observability"
	pb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
)

func projectPause(p *observability.LifecyclePause) *pb.HealthPause {
	if p == nil {
		return nil
	}
	return &pb.HealthPause{Kind: p.Kind, Reason: p.Reason, By: p.By, Since: p.Since}
}

func projectCatchup(c *observability.LifecycleCatchup) *pb.HealthCatchup {
	if c == nil {
		return nil
	}
	return &pb.HealthCatchup{
		Since: c.Since, Done: c.Done, Total: c.Total, CaughtUp: c.CaughtUp,
		BlockedBy: c.BlockedBy, Unfillable: int32(c.Unfillable), Message: c.Message,
	}
}

// projectLifecycle 把模块上报的生命周期状态投影成概览里的组件字段。
func projectLifecycle(l *lifecycleSource) *pb.HealthLifecycle {
	if l == nil {
		return nil
	}
	return &pb.HealthLifecycle{Stage: l.Stage, Reason: l.Reason, Pause: projectPause(l.Pause), Catchup: projectCatchup(l.Catchup)}
}

// lifecycleModules 是提供生命周期接口的模块（组件 ID）。
var lifecycleModules = map[string]bool{"collector": true, "factor-engine": true, "strategy": true, "trade": true, "factor-mgr": true, "monitor": true, "archive-mgr": true}

// systemPausable 是系统级暂停（moox-cli system pause）覆盖的模块（组件 ID）。
var systemPausable = map[string]bool{"collector": true, "factor-engine": true, "strategy": true, "trade": true}

// projectSystem 汇总系统状态（设计 3.8），数据全部来自各模块的上报：
//   - 健康面：有生命周期接口的模块停止上报（或从未上报）、上报了未就绪时标红，并列出模块与原因；
//   - 运行阶段：各模块只有运行、暂停、恢复追赶三种状态。有模块恢复追赶 → resuming（含「已追平待放行」）；
//     系统暂停覆盖的模块全部暂停 → paused，部分暂停 → partially_paused；否则 running。
//
// 没有任何模块上报生命周期状态时返回 nil。
func projectSystem(components []*pb.HealthComponent) *pb.HealthSystem {
	type module struct {
		id, name string
		l        *pb.HealthLifecycle
	}
	var modules []module
	// silent 是停止上报（或从未上报）的模块，notReady 是上报了未就绪的模块，都是“模块：说明”。
	var silent, notReady []string
	counted := 0
	for _, c := range components {
		if c.GetStatus() == "disabled" {
			continue
		}
		name := firstNonEmpty(c.GetName(), c.GetComponentId())
		issue := false
		if lifecycleModules[c.GetComponentId()] {
			switch {
			case c.GetReporter().GetStatus() != "healthy":
				// 它的生命周期状态读不出来，但不能因此从系统汇总里消失（否则其余模块还在跑时首页仍是绿色）。
				silent, issue = append(silent, name+"：停止上报"), true
			case c.GetReadiness() == "not_ready":
				notReady, issue = append(notReady, name+"：未就绪（"+firstNonEmpty(c.GetNotReadyReason(), "组件报告未就绪")+"）"), true
			}
		}
		if c.GetLifecycle() != nil {
			modules = append(modules, module{id: c.GetComponentId(), name: name, l: c.GetLifecycle()})
		}
		if c.GetLifecycle() != nil || issue {
			counted++
		}
	}
	if counted == 0 {
		return nil
	}
	sort.Slice(modules, func(i, j int) bool { return modules[i].name < modules[j].name })
	sort.Strings(silent)
	sort.Strings(notReady)
	out := &pb.HealthSystem{Health: "healthy", Phase: "running", ModuleCount: int32(counted), PauseKinds: map[string]int32{},
		NotReadyModules: int32(len(notReady)), SilentModules: int32(len(silent))}
	if len(silent)+len(notReady) > 0 {
		out.Health = "attention"
		out.Issues = append(append(out.Issues, silent...), notReady...)
	}
	if len(modules) == 0 {
		out.Phase = "unknown"
	}
	var pausedModules, pausableModules, resuming, caughtUp int
	for _, m := range modules {
		l := m.l
		// 系统暂停动作只覆盖采集、因子引擎、策略、Trade：「全部暂停」按它们来判断，
		// 因子管理、Monitor 这类不参与系统暂停的模块不能让系统永远停在「部分暂停」。
		pausable := systemPausable[m.id]
		if pausable {
			pausableModules++
		}
		switch l.Stage {
		case "paused":
			if pausable {
				pausedModules++
			}
			if l.Pause != nil && l.Pause.Kind != "" {
				out.PauseKinds[l.Pause.Kind]++
			}
			if l.Pause != nil && l.Pause.Since > 0 && (out.PausedSince == 0 || l.Pause.Since < out.PausedSince) {
				out.PausedSince = l.Pause.Since
			}
			out.Issues = append(out.Issues, fmt.Sprintf("%s：%s", m.name, firstNonEmpty(l.Reason, "已暂停")))
		case "resuming":
			resuming++
			if l.Catchup != nil && l.Catchup.CaughtUp {
				caughtUp++
			}
			out.CatchupNotes = append(out.CatchupNotes, catchupNote(m.name, l.Catchup))
		}
	}
	out.ResumingModules, out.CaughtUpModules = int32(resuming), int32(caughtUp)
	switch {
	case resuming > 0:
		out.Phase = "resuming"
	case pausedModules > 0 && pausedModules == pausableModules:
		out.Phase = "paused"
	case pausedModules > 0:
		out.Phase = "partially_paused"
	}
	switch {
	case out.Health == "attention":
		out.Color = "red"
	case out.Phase == "resuming":
		out.Color = "cyan"
	case out.Phase == "paused" || out.Phase == "partially_paused":
		out.Color = "orange"
	default:
		out.Color = "green"
	}
	if len(out.PauseKinds) == 0 {
		out.PauseKinds = nil
	}
	return out
}

// catchupNote 是一个恢复追赶中的模块的进度文字：已追平待放行、被谁阻塞、或已完成多少。
func catchupNote(name string, c *pb.HealthCatchup) string {
	switch {
	case c == nil:
		return name + "：追赶中"
	case c.GetCaughtUp():
		return name + "：已追平，待放行"
	case c.GetBlockedBy() != "":
		return fmt.Sprintf("%s：被 %s 阻塞", name, c.GetBlockedBy())
	case c.GetTotal() > 0:
		return fmt.Sprintf("%s：补采 %d/%d", name, c.GetDone(), c.GetTotal())
	case c.GetMessage() != "":
		return name + "：" + c.GetMessage()
	}
	return name + "：追赶中"
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// lifecycleSource 是 observability.LifecycleStatus 的别名，避免在投影函数里反复写包名。
type lifecycleSource = observability.LifecycleStatus
