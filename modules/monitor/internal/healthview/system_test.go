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

func TestProjectSystemColorsAndPhase(t *testing.T) {
	require.Nil(t, projectSystem([]*pb.HealthComponent{{ComponentId: "storage"}}), "没有模块上报生命周期时不输出系统状态")

	running := projectSystem([]*pb.HealthComponent{component("因子引擎", "running"), component("策略", "running")})
	require.Equal(t, "running", running.Phase)
	require.Equal(t, "green", running.Color)
	require.Equal(t, "healthy", running.Health)

	// 一个模块还只是就绪：系统的运行阶段取最靠前的。
	mixed := projectSystem([]*pb.HealthComponent{component("因子引擎", "running"), component("策略", "ready")})
	require.Equal(t, "ready", mixed.Phase)
	require.Equal(t, "blue", mixed.Color)

	warming := projectSystem([]*pb.HealthComponent{component("因子引擎", "warming"), component("策略", "running")})
	require.Equal(t, "yellow", warming.Color)
}

// 运行中、有对象暂停：运行中 · 降级（橙）；有模块未就绪或故障：红并列出原因。
func TestProjectSystemDegradedAndAttention(t *testing.T) {
	degraded := projectSystem([]*pb.HealthComponent{
		component("因子引擎", "running", func(l *pb.HealthLifecycle) {
			l.Degraded = true
			l.Attention = []*pb.HealthLifecycleObject{{Id: "crypto_1m", Stage: "paused", Reason: "变更同步：变更日志断档"}}
		}),
		component("策略", "running"),
	})
	require.Equal(t, "running", degraded.Phase)
	require.True(t, degraded.Degraded)
	require.Equal(t, "orange", degraded.Color)
	require.Equal(t, "healthy", degraded.Health)
	require.Contains(t, degraded.Issues[0], "因子引擎：crypto_1m 变更同步：变更日志断档")

	bad := projectSystem([]*pb.HealthComponent{
		component("因子引擎", "running"),
		component("Trade", "not_ready", func(l *pb.HealthLifecycle) { l.Reason = "数据库不可用" }),
	})
	require.Equal(t, "attention", bad.Health)
	require.Equal(t, "red", bad.Color)
	require.Equal(t, []string{"Trade：数据库不可用"}, bad.Issues)

	recovered := projectSystem([]*pb.HealthComponent{component("因子引擎", "running", func(l *pb.HealthLifecycle) { l.Recovered = 2 })})
	require.True(t, recovered.AutoRecovered)
}

// 有生命周期接口的模块探测不到：不能从系统汇总里消失，健康面标红并列出。
func TestProjectSystemReportsUnreachableLifecycleModule(t *testing.T) {
	down := &pb.HealthComponent{ComponentId: "strategy", Name: "策略", Status: "down"}
	system := projectSystem([]*pb.HealthComponent{component("因子引擎", "running"), down, {ComponentId: "storage", Status: "down"}})
	require.Equal(t, "attention", system.Health)
	require.Equal(t, "red", system.Color)
	require.Equal(t, []string{"策略：探测不到（模块没有响应）"}, system.Issues, "没有生命周期接口的组件不在此列")
	require.Equal(t, int32(2), system.ModuleCount)

	only := projectSystem([]*pb.HealthComponent{down})
	require.Equal(t, "attention", only.Health)
	require.Equal(t, "uninitialized", only.Phase)
}

// 手动、系统暂停是用户意图：不降级，首页显示暂停；全部暂停是 paused，部分暂停是 partially_paused。
func TestProjectSystemPausePhases(t *testing.T) {
	paused := func(kind string) func(*pb.HealthLifecycle) {
		return func(l *pb.HealthLifecycle) {
			l.Reason = "系统暂停：切换行情源"
			l.Pause = &pb.HealthPause{Kind: kind, Since: "2026-10-21T10:00:00Z"}
			l.PauseKinds = map[string]int32{kind: 1}
		}
	}
	all := projectSystem([]*pb.HealthComponent{component("因子引擎", "paused", paused("system")), component("策略", "paused", paused("system"))})
	require.Equal(t, "paused", all.Phase)
	require.Equal(t, "orange", all.Color)
	require.False(t, all.Degraded, "系统暂停不算降级")
	require.Equal(t, "healthy", all.Health)
	require.Equal(t, int32(2), all.PauseKinds["system"])
	require.Equal(t, "2026-10-21T10:00:00Z", all.PausedSince)

	partial := projectSystem([]*pb.HealthComponent{component("因子引擎", "running"), component("策略", "paused", paused("manual"))})
	require.Equal(t, "partially_paused", partial.Phase)
	require.False(t, partial.Degraded)
	require.Contains(t, partial.Issues[0], "策略：系统暂停")

	// 对象级的手动暂停：模块汇总仍是运行，pause_kinds 计数但不降级。
	object := projectSystem([]*pb.HealthComponent{component("策略", "running", func(l *pb.HealthLifecycle) {
		l.PauseKinds = map[string]int32{"manual": 1}
	})})
	require.Equal(t, "running", object.Phase)
	require.Equal(t, int32(1), object.PauseKinds["manual"])
	require.False(t, object.Degraded)
}

func TestProjectSystemResumingAndReset(t *testing.T) {
	resuming := projectSystem([]*pb.HealthComponent{
		component("因子引擎", "resuming", func(l *pb.HealthLifecycle) {
			l.Catchup = &pb.HealthCatchup{Done: 6, Total: 10}
		}),
		component("策略", "resuming", func(l *pb.HealthLifecycle) {
			l.Catchup = &pb.HealthCatchup{BlockedBy: "factor_set/crypto_1m"}
		}),
		component("Trade", "resuming", func(l *pb.HealthLifecycle) { l.Catchup = &pb.HealthCatchup{CaughtUp: true} }),
	})
	require.Equal(t, "resuming", resuming.Phase)
	require.Equal(t, "cyan", resuming.Color)
	require.False(t, resuming.Degraded, "恢复追赶是预期的过渡，不算降级")
	require.Equal(t, int32(3), resuming.ResumingModules)
	require.Equal(t, int32(1), resuming.CaughtUpModules)
	require.ElementsMatch(t, []string{"因子引擎：补采 6/10", "策略：被 factor_set/crypto_1m 阻塞", "Trade：已追平，待放行"}, resuming.CatchupNotes)

	resetting := projectSystem([]*pb.HealthComponent{component("因子引擎", "resetting"), component("策略", "running")})
	require.Equal(t, "resetting", resetting.Phase)
	require.Equal(t, "purple", resetting.Color)

	failed := projectSystem([]*pb.HealthComponent{component("因子引擎", "reset_failed", func(l *pb.HealthLifecycle) { l.Reason = "清理本地存储失败" })})
	require.Equal(t, "attention", failed.Health)
	require.Equal(t, "red", failed.Color)
	require.Equal(t, []string{"因子引擎：清理本地存储失败"}, failed.Issues)
}
