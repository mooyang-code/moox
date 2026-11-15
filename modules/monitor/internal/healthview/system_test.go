package healthview

import (
	"testing"

	pb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/stretchr/testify/require"
)

func component(name, stage string, mutate ...func(*pb.HealthLifecycle)) *pb.HealthComponent {
	l := &pb.HealthLifecycle{Stage: stage}
	for _, m := range mutate {
		m(l)
	}
	return &pb.HealthComponent{ComponentId: name, Name: name, Lifecycle: l}
}

func TestProjectSystemRunning(t *testing.T) {
	require.Nil(t, projectSystem([]*pb.HealthComponent{{ComponentId: "storage"}}), "没有模块上报生命周期时不输出系统状态")

	running := projectSystem([]*pb.HealthComponent{component("因子引擎", "running"), component("策略", "running"), component("采集", "running")})
	require.Equal(t, "running", running.Phase)
	require.Equal(t, "green", running.Color)
	require.Equal(t, "healthy", running.Health)
	require.Empty(t, running.Issues)
	require.Nil(t, running.PauseKinds)
}

// 有生命周期接口的模块探测不到：不能从系统汇总里消失，健康面标红并列出。
func TestProjectSystemReportsUnreachableLifecycleModule(t *testing.T) {
	down := &pb.HealthComponent{ComponentId: "strategy", Name: "策略", Status: "down"}
	system := projectSystem([]*pb.HealthComponent{component("因子引擎", "running"), down, {ComponentId: "storage", Status: "down"}})
	require.Equal(t, "attention", system.Health)
	require.Equal(t, "red", system.Color)
	require.Equal(t, "running", system.Phase)
	require.Equal(t, []string{"策略：探测不到（模块没有响应）"}, system.Issues, "没有生命周期接口的组件不在此列")
	require.Equal(t, int32(2), system.ModuleCount)

	only := projectSystem([]*pb.HealthComponent{down})
	require.Equal(t, "attention", only.Health)
	require.Equal(t, "unknown", only.Phase, "一个模块都读不到时运行阶段未知")
}

// 系统暂停覆盖的模块全部暂停是 paused，部分暂停是 partially_paused；不参与系统暂停的模块不影响“全部”的判断。
func TestProjectSystemPausePhases(t *testing.T) {
	paused := func(kind string) func(*pb.HealthLifecycle) {
		return func(l *pb.HealthLifecycle) {
			l.Reason = "系统暂停：切换行情源"
			l.Pause = &pb.HealthPause{Kind: kind, Since: "2026-10-21T10:00:00Z"}
		}
	}
	all := projectSystem([]*pb.HealthComponent{component("factor-engine", "paused", paused("system")), component("strategy", "paused", paused("system")), component("factor-mgr", "running")})
	require.Equal(t, "paused", all.Phase)
	require.Equal(t, "orange", all.Color)
	require.Equal(t, "healthy", all.Health)
	require.Equal(t, int32(2), all.PauseKinds["system"])
	require.Equal(t, "2026-10-21T10:00:00Z", all.PausedSince)
	require.Equal(t, []string{"factor-engine：系统暂停：切换行情源", "strategy：系统暂停：切换行情源"}, all.Issues)

	partial := projectSystem([]*pb.HealthComponent{component("factor-engine", "running"), component("strategy", "paused", paused("manual"))})
	require.Equal(t, "partially_paused", partial.Phase)
	require.Equal(t, "orange", partial.Color)
	require.Equal(t, int32(1), partial.PauseKinds["manual"])
	require.Contains(t, partial.Issues[0], "strategy：系统暂停")

	// 只有不参与系统暂停的模块（例如 Monitor 的告警屏蔽）暂停：不算系统部分暂停，但列出来。
	muted := projectSystem([]*pb.HealthComponent{component("factor-engine", "running"), component("monitor", "paused", func(l *pb.HealthLifecycle) { l.Reason = "手动暂停：告警屏蔽" })})
	require.Equal(t, "running", muted.Phase)
	require.Equal(t, []string{"monitor：手动暂停：告警屏蔽"}, muted.Issues)
}

// 恢复追赶：显示追赶中的模块数与已追平（待放行）的模块数，以及各模块的进度文字。
func TestProjectSystemResumingAndCaughtUp(t *testing.T) {
	resuming := projectSystem([]*pb.HealthComponent{
		component("因子引擎", "resuming", func(l *pb.HealthLifecycle) {
			l.Catchup = &pb.HealthCatchup{Done: 6, Total: 10}
		}),
		component("策略", "resuming", func(l *pb.HealthLifecycle) {
			l.Catchup = &pb.HealthCatchup{BlockedBy: "实例 i1"}
		}),
		component("Trade", "resuming", func(l *pb.HealthLifecycle) { l.Catchup = &pb.HealthCatchup{CaughtUp: true} }),
	})
	require.Equal(t, "resuming", resuming.Phase)
	require.Equal(t, "cyan", resuming.Color)
	require.Equal(t, int32(3), resuming.ResumingModules)
	require.Equal(t, int32(1), resuming.CaughtUpModules)
	require.ElementsMatch(t, []string{"因子引擎：补采 6/10", "策略：被 实例 i1 阻塞", "Trade：已追平，待放行"}, resuming.CatchupNotes)

	// 全部追平、等待放行。
	caught := projectSystem([]*pb.HealthComponent{
		component("因子引擎", "resuming", func(l *pb.HealthLifecycle) { l.Catchup = &pb.HealthCatchup{CaughtUp: true} }),
		component("采集", "running"),
	})
	require.Equal(t, "resuming", caught.Phase)
	require.Equal(t, caught.ResumingModules, caught.CaughtUpModules)
	require.Equal(t, []string{"因子引擎：已追平，待放行"}, caught.CatchupNotes)
}
