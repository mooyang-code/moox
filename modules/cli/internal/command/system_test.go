package command

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/stretchr/testify/require"
)

// systemFixture 是 H4 的跨模块编排夹具：四个参与系统暂停的模块与两个不参与的管理端，都是真实的生命周期控制器，
// 命令行经操作员网关（替身）调各模块服务的 GetLifecycle、ControlLifecycle。
type systemFixture struct {
	gateway *lifecycleGateway
	ctrls   map[string]*lifecycle.Controller
	// polls 是各模块追赶钩子被调用的次数；caughtAfter 是第几次调用起报告追平。
	polls       map[string]*atomic.Int32
	caughtAfter map[string]int32
	// quiesce 让模块暂停后的收敛失败（在途操作没有结束）。
	quiesce map[string]error
}

func newSystemFixture(t *testing.T, modules ...string) *systemFixture {
	t.Helper()
	f := &systemFixture{gateway: newLifecycleGateway(), ctrls: map[string]*lifecycle.Controller{}, polls: map[string]*atomic.Int32{},
		caughtAfter: map[string]int32{}, quiesce: map[string]error{}}
	for _, name := range modules {
		polls := &atomic.Int32{}
		f.polls[name] = polls
		f.ctrls[name] = f.gateway.add(t, name, lifecycle.Hooks{
			Catchup: func(context.Context, time.Time) (lifecycle.Catchup, error) {
				n := polls.Add(1)
				return lifecycle.Catchup{Total: 10, Done: int64(min(n, 10)), CaughtUp: n >= f.caughtAfter[name]}, nil
			},
			Quiesce: func(context.Context) error { return f.quiesce[name] },
		})
	}
	f.gateway.use(t)
	return f
}

var systemAll = []string{"collector", "factor-engine", "strategy", "trade", "factor-mgr", "archive-mgr"}

// H4：系统暂停按策略 → Trade → 因子引擎 → 采集逐个暂停；恢复时上游追平后下游才开始追赶（采集追平后因子才开始、因子追平后策略才开始，
// Trade 一开始就追），全部追平后按采集（自行放行）、因子引擎、Trade、策略的顺序放行。factor-mgr、archive-mgr 不参与。
func TestSystemPauseAndResumeAcrossModulesInOrder(t *testing.T) {
	f := newSystemFixture(t, systemAll...)
	f.caughtAfter["collector"], f.caughtAfter["factor-engine"] = 3, 2
	ctx := context.Background()
	opts := systemOptions{Reason: "切换行情源", By: "ops"}
	var out bytes.Buffer

	require.NoError(t, runSystemPause(ctx, opts, &out))
	require.Contains(t, out.String(), "系统已暂停：[strategy trade factor-engine collector]")
	require.Equal(t, []string{"strategy:pause", "trade:pause", "factor-engine:pause", "collector:pause"}, f.gateway.snapshot())
	for _, name := range []string{"collector", "factor-engine", "strategy", "trade"} {
		state := f.ctrls[name].State()
		require.Equal(t, lifecycle.StagePaused, state.Stage, name)
		require.Equal(t, lifecycle.PauseSystem, state.Pause.Kind, name)
		require.Equal(t, "切换行情源", state.Pause.Reason, name)
		require.True(t, state.Quiesced(), name)
	}
	require.True(t, f.ctrls["factor-mgr"].Running(), "factor-mgr 不参与系统暂停")
	require.True(t, f.ctrls["archive-mgr"].Running(), "archive-mgr 不参与系统暂停")

	out.Reset()
	require.NoError(t, runSystemStatus(ctx, opts, &out))
	require.Contains(t, out.String(), "系统暂停：切换行情源")

	f.gateway.calls = nil
	out.Reset()
	require.NoError(t, runSystemResume(ctx, opts, &out))
	require.Contains(t, out.String(), "系统已恢复")
	calls := f.gateway.snapshot()
	require.Less(t, indexOf(calls, "trade:resume"), indexOf(calls, "factor-engine:resume"), "Trade 一开始就追赶")
	require.Less(t, indexOf(calls, "collector:resume"), indexOf(calls, "factor-engine:resume"), "采集追平后因子才开始")
	require.Less(t, indexOf(calls, "factor-engine:resume"), indexOf(calls, "strategy:resume"), "因子追平后策略才开始")
	require.GreaterOrEqual(t, f.polls["collector"].Load(), int32(3), "采集追平之前因子没有开始")
	require.Equal(t, -1, indexOf(calls, "collector:release"), "采集补齐暂停区间就自行放行")
	lastResume := max(indexOf(calls, "factor-engine:resume"), indexOf(calls, "strategy:resume"), indexOf(calls, "trade:resume"))
	require.Greater(t, indexOf(calls, "factor-engine:release"), lastResume, "全部追平之后才放行")
	require.Less(t, indexOf(calls, "factor-engine:release"), indexOf(calls, "trade:release"))
	require.Less(t, indexOf(calls, "trade:release"), indexOf(calls, "strategy:release"), "Trade 先于策略放行")
	for _, name := range systemAll {
		require.Equal(t, lifecycle.StageRunning, f.ctrls[name].Stage(), name)
	}
	for _, call := range calls {
		require.NotContains(t, call, "factor-mgr")
		require.NotContains(t, call, "archive-mgr")
	}
}

// 暂停中途的失败与原来一致：
//   - 策略的在途目标没发完：停在策略这一步，Trade 及更上游都不暂停（策略自己已暂停）；
//   - Trade 的在途订单没结束：只是提示，继续暂停上游；
//   - 某个模块不可达：停在那里，已暂停的不回滚。
func TestSystemPauseFailuresAcrossModules(t *testing.T) {
	ctx := context.Background()
	var out bytes.Buffer

	f := newSystemFixture(t, systemAll...)
	f.quiesce["strategy"] = errors.New("还有 2 个目标没有发布到 EventBus")
	err := runSystemPause(ctx, systemOptions{}, &out)
	require.ErrorContains(t, err, "暂停 strategy")
	require.ErrorContains(t, err, "已暂停，但在途操作没有结束")
	require.Equal(t, []string{"strategy:pause"}, f.gateway.snapshot(), "Trade、因子引擎、采集都不能在策略的目标发完之前暂停")
	require.True(t, f.ctrls["strategy"].Halted())
	require.True(t, f.ctrls["trade"].Running())
	delete(f.quiesce, "strategy")
	require.NoError(t, runSystemPause(ctx, systemOptions{}, &out), "目标发完后重新执行：幂等，继续往上游暂停")
	require.True(t, f.ctrls["collector"].Halted())

	g := newSystemFixture(t, systemAll...)
	g.quiesce["trade"] = errors.New("仍有 1 个目标在收敛")
	out.Reset()
	require.NoError(t, runSystemPause(ctx, systemOptions{}, &out))
	require.Contains(t, out.String(), "提示：trade：已暂停，但在途操作没有结束")
	require.Contains(t, out.String(), "系统已暂停：[strategy trade factor-engine collector]")

	h := newSystemFixture(t, systemAll...)
	module, _ := lifecycleModuleByName("factor-engine")
	h.gateway.failures[module.Service] = errors.New("relay: factor host is offline")
	out.Reset()
	err = runSystemPause(ctx, systemOptions{}, &out)
	require.ErrorContains(t, err, "暂停 factor-engine")
	require.Contains(t, out.String(), "已暂停：[strategy trade]")
	require.True(t, h.ctrls["strategy"].Halted())
	require.True(t, h.ctrls["trade"].Halted())
	require.True(t, h.ctrls["collector"].Running(), "停在不可达的因子引擎，采集没有暂停")
}

// 服务目录里没有部署的模块跳过（例如还没装因子机）；一个都没有时报错。追赶超时不放行任何有副作用的模块。
func TestSystemSkipsUndeployedModulesAndTimesOut(t *testing.T) {
	ctx := context.Background()
	var out bytes.Buffer
	f := newSystemFixture(t, "collector", "strategy", "trade")
	require.NoError(t, runSystemPause(ctx, systemOptions{}, &out))
	require.Contains(t, out.String(), "服务目录里没有部署 factor-engine，已跳过")
	require.Contains(t, out.String(), "系统已暂停：[strategy trade collector]")

	f.caughtAfter["strategy"] = 1 << 30 // 策略一直追不平
	out.Reset()
	err := runSystemResume(ctx, systemOptions{Timeout: time.Nanosecond}, &out)
	require.ErrorContains(t, err, "部分模块没有追平")
	require.Contains(t, out.String(), "等待追平超时，没有自动放行")
	require.Equal(t, lifecycle.StageResuming, f.ctrls["trade"].Stage(), "超时不自动放行")
	require.Equal(t, lifecycle.StageRunning, f.ctrls["collector"].Stage(), "采集补齐暂停区间就自行放行")

	newSystemFixture(t)
	require.ErrorContains(t, runSystemStatus(ctx, systemOptions{}, &out), "没有可操作的模块")
	require.ErrorContains(t, runSystemResume(ctx, systemOptions{ReleasePartial: true}, &out), "--release-partial 会在超时后放行")
}
