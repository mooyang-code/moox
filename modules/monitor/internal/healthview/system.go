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
	out := &pb.HealthLifecycle{
		Stage: l.Stage, Color: l.Color, Degraded: l.Degraded, Reason: l.Reason, SelfStage: l.Self, Recovered: int32(l.Recovered),
		Pause: projectPause(l.Pause), Hold: l.Hold, Catchup: projectCatchup(l.Catchup),
		Counts: map[string]int32{}, PauseKinds: map[string]int32{},
	}
	for stage, n := range l.Counts {
		out.Counts[stage] = int32(n)
	}
	for kind, n := range l.PauseKinds {
		out.PauseKinds[kind] = int32(n)
	}
	for _, o := range l.Attention {
		item := &pb.HealthLifecycleObject{
			Kind: o.Kind, Id: o.ID, Stage: o.Stage, Color: o.Color, Reason: o.Reason, Effective: o.Effective,
			Pause: projectPause(o.Pause), Catchup: projectCatchup(o.Catchup),
		}
		for _, c := range o.Checks {
			text := c.Name
			if c.Message != "" {
				text += "：" + c.Message
			}
			if c.Remedy != "" {
				text += "（处理：" + c.Remedy + "）"
			}
			item.FailedChecks = append(item.FailedChecks, text)
		}
		out.Attention = append(out.Attention, item)
	}
	return out
}

// lifecycleModules 是提供生命周期接口的模块（组件 ID）。
var lifecycleModules = map[string]bool{"collector": true, "factor-engine": true, "strategy": true, "trade": true, "factor-mgr": true, "monitor": true, "archive-mgr": true}

// systemPausable 是系统级暂停（moox-cli system pause）覆盖的模块（组件 ID）。
var systemPausable = map[string]bool{"collector": true, "factor-engine": true, "strategy": true, "trade": true}

// phaseRank 是运行阶段的先后：系统的运行阶段取各模块中最靠前的（只要有一个模块还没到运行，系统就没有完全运行）。
var phaseRank = map[string]int{"uninitialized": 0, "not_ready": 0, "ready": 1, "warming": 2, "running": 3}

// projectSystem 汇总系统状态（设计 6.1）：
//   - 健康面：任何模块未就绪、重置失败或故障，系统标红，并列出模块与原因；
//   - 运行阶段：就绪 → 预热 → 运行，各模块里最靠前的；暂停、恢复追赶、重置是叠加在上面的控制状态：
//     有模块在重置 → resetting；有模块恢复追赶 → resuming（含「已追平待放行」）；全部可暂停模块暂停 → paused，部分暂停 → partially_paused；
//   - 降级只算不是用户意图的情形（自动暂停、故障）；手动、系统暂停和恢复追赶都不算降级。
//
// 没有任何模块上报生命周期状态时返回 nil。
func projectSystem(components []*pb.HealthComponent) *pb.HealthSystem {
	type module struct {
		id, name string
		l        *pb.HealthLifecycle
	}
	var modules []module
	var unreachable []string
	for _, c := range components {
		if c.GetLifecycle() == nil && lifecycleModules[c.GetComponentId()] && (c.GetStatus() == "down" || c.GetStatus() == "unknown") {
			// 有生命周期接口的模块探测不到：它的生命周期状态读不出来，但不能因此从系统汇总里消失（否则其余模块还在跑时首页仍是绿色）。
			unreachable = append(unreachable, firstNonEmpty(c.GetName(), c.GetComponentId()))
			continue
		}
		if c.GetLifecycle() != nil {
			name := c.GetName()
			if name == "" {
				name = c.GetComponentId()
			}
			modules = append(modules, module{id: c.GetComponentId(), name: name, l: c.GetLifecycle()})
		}
	}
	if len(modules) == 0 && len(unreachable) == 0 {
		return nil
	}
	sort.Slice(modules, func(i, j int) bool { return modules[i].name < modules[j].name })
	sort.Strings(unreachable)
	out := &pb.HealthSystem{Health: "healthy", ModuleCount: int32(len(modules) + len(unreachable)), PauseKinds: map[string]int32{}}
	for _, name := range unreachable {
		out.Health = "attention"
		out.Issues = append(out.Issues, fmt.Sprintf("%s：探测不到（模块没有响应）", name))
	}
	phase := 3
	phaseName := "running"
	if len(modules) == 0 {
		phase, phaseName = 0, "uninitialized"
	}
	var pausedModules, pausableModules, resuming, caughtUp, resetting int
	var pausedSince string
	for _, m := range modules {
		l := m.l
		bad := l.Stage == "not_ready" || l.Stage == "fault" || l.Stage == "reset_failed"
		if bad {
			out.Health = "attention"
			reason := l.Reason
			if len(l.Attention) > 0 {
				reason = fmt.Sprintf("%s %s", l.Attention[0].Id, firstNonEmpty(l.Attention[0].Reason, l.Reason))
			}
			out.Issues = append(out.Issues, fmt.Sprintf("%s：%s", m.name, firstNonEmpty(reason, l.Stage)))
		}
		if l.Degraded && !bad {
			out.Degraded = true
			if len(l.Attention) > 0 {
				out.Issues = append(out.Issues, fmt.Sprintf("%s：%s %s", m.name, l.Attention[0].Id, firstNonEmpty(l.Attention[0].Reason, l.Attention[0].Stage)))
			}
		}
		for kind, n := range l.PauseKinds {
			out.PauseKinds[kind] += n
		}
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
			note := firstNonEmpty(l.Reason, "已暂停")
			if l.Pause != nil && l.Pause.Since != "" && (pausedSince == "" || l.Pause.Since < pausedSince) {
				pausedSince = l.Pause.Since
			}
			if !l.Degraded {
				out.Issues = append(out.Issues, fmt.Sprintf("%s：%s", m.name, note))
			}
		case "resuming":
			resuming++
			caught := l.Catchup != nil && l.Catchup.CaughtUp
			if caught {
				caughtUp++
			}
			out.CatchupNotes = append(out.CatchupNotes, catchupNote(m.name, l.Catchup))
		case "resetting":
			resetting++
		}
		if l.Recovered > 0 {
			out.AutoRecovered = true
		}
		// 没有对象、自身只是「就绪」的模块（采集、因子管理这类没有预热和运行阶段的模块）不参与运行阶段的比较：
		// 它们没有「启动」这一步，让它们拉低运行阶段会让整个系统一直显示「就绪」。
		if l.Stage == "ready" && len(l.Counts) == 0 {
			continue
		}
		if rank, ok := phaseRank[l.Stage]; ok && rank < phase {
			phase, phaseName = rank, l.Stage
			if l.Stage == "not_ready" {
				phaseName = "uninitialized"
			}
		}
	}
	out.Phase = phaseName
	out.PausedSince = pausedSince
	out.ResumingModules, out.CaughtUpModules = int32(resuming), int32(caughtUp)
	switch {
	case resetting > 0:
		out.Phase = "resetting"
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
	case out.Phase == "resetting":
		out.Color = "purple"
	case out.Phase == "resuming":
		out.Color = "cyan"
	case out.Degraded || out.Phase == "paused" || out.Phase == "partially_paused":
		out.Color = "orange"
	case out.Phase == "warming":
		out.Color = "yellow"
	case out.Phase == "ready":
		out.Color = "blue"
	case out.Phase == "running":
		out.Color = "green"
	default:
		out.Color = "gray"
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
