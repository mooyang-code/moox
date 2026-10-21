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
