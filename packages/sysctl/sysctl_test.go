package sysctl

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// moduleAPI 是一个被测模块：真实的生命周期控制器，经测试网关按 RPC 调用（*lifecycle.Remote → 网关 → 控制器的 RPC 实现）。
type moduleAPI struct {
	c    *lifecycle.Controller
	name string
	// callErr 让对这个模块的调用在网关上失败（模块不可达），请求不会到达模块。
	callErr error
	// quiesceErr 让暂停之后的收敛失败：门禁已经关闭，模块回答“已暂停，但在途操作没有结束”。
	quiesceErr error
}

type callLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *callLog) add(s string) { l.mu.Lock(); l.calls = append(l.calls, s); l.mu.Unlock() }
func (l *callLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

// moduleGateway 是测试用的网关：按服务名把请求交给模块控制器的 RPC 实现，请求与响应都过一遍 protobuf 编码；记下每个命令。
type moduleGateway struct {
	log     *callLog
	modules map[string]*moduleAPI
}

func (g *moduleGateway) Invoke(ctx context.Context, service, method string, req, rsp any) error {
	m := g.modules[service]
	if m == nil {
		return errors.New("service has no enabled deployment")
	}
	if m.callErr != nil {
		return m.callErr
	}
	raw, err := proto.Marshal(req.(proto.Message))
	if err != nil {
		return err
	}
	var out proto.Message
	switch method {
	case lifecycle.GetMethod:
		in := &commonpb.GetLifecycleReq{}
		if err := proto.Unmarshal(raw, in); err != nil {
			return err
		}
		out, _ = m.c.GetLifecycle(ctx, in)
	case lifecycle.ControlMethod:
		in := &commonpb.ControlLifecycleReq{}
		if err := proto.Unmarshal(raw, in); err != nil {
			return err
		}
		g.log.add(m.name + ":" + in.GetCommand())
		out, _ = m.c.ControlLifecycle(ctx, in)
	default:
		return errors.New("unknown method " + method)
	}
	encoded, err := proto.Marshal(out)
	if err != nil {
		return err
	}
	return proto.Unmarshal(encoded, rsp.(proto.Message))
}

type fixture struct {
	o      *Orchestrator
	log    *callLog
	caught map[string]*bool
	apis   map[string]*moduleAPI
	out    *bytes.Buffer
}

func newFixture(t *testing.T, names ...string) *fixture {
	t.Helper()
	f := &fixture{log: &callLog{}, caught: map[string]*bool{}, apis: map[string]*moduleAPI{}, out: &bytes.Buffer{}}
	gateway := &moduleGateway{log: f.log, modules: map[string]*moduleAPI{}}
	var mods []Module
	for _, name := range names {
		flag := new(bool)
		f.caught[name] = flag
		api := &moduleAPI{name: name}
		c, err := lifecycle.New(lifecycle.Options{Module: name, Hooks: lifecycle.Hooks{
			Catchup: func(context.Context, time.Time) (lifecycle.Catchup, error) {
				return lifecycle.Catchup{CaughtUp: *flag, Total: 10}, nil
			},
			Quiesce: func(context.Context) error { return api.quiesceErr },
		}})
		require.NoError(t, err)
		c.Refresh(context.Background())
		api.c = c
		f.apis[name] = api
		service := "trpc.moox.test." + name
		gateway.modules[service] = api
		mods = append(mods, Module{Name: name, API: &lifecycle.Remote{Gateway: gateway, Service: service}})
	}
	f.o = &Orchestrator{Modules: mods, By: "test", Out: f.out, Poll: time.Millisecond, Sleep: func(ctx context.Context, d time.Duration) error { return ctx.Err() }}
	return f
}

func (f *fixture) index(call string) int {
	for i, c := range f.log.snapshot() {
		if c == call {
			return i
		}
	}
	return -1
}

var all = []string{Collector, FactorEngine, Strategy, Trade}

// 暂停：下游到上游（策略 → Trade → 因子 → 采集），带系统类型与原因。
func TestPauseGoesDownstreamToUpstream(t *testing.T) {
	f := newFixture(t, all...)
	result, err := f.o.Pause(context.Background(), "切换行情源")
	require.NoError(t, err)
	require.Equal(t, []string{Strategy, Trade, FactorEngine, Collector}, result.Paused)
	require.Equal(t, []string{"strategy:pause", "trade:pause", "factor-engine:pause", "collector:pause"}, f.log.snapshot())
	for _, name := range all {
		snap := f.apis[name].c.State()
		require.Equal(t, lifecycle.StagePaused, snap.Stage, name)
		require.Equal(t, lifecycle.PauseSystem, snap.Pause.Kind, name)
		require.Equal(t, "切换行情源", snap.Pause.Reason)
		require.Equal(t, "test", snap.Pause.By)
	}
	// 幂等：再暂停一次不报错。
	_, err = f.o.Pause(context.Background(), "切换行情源")
	require.NoError(t, err)
}

// 某个模块暂停失败：停在那里，不回滚已暂停的部分；在途操作没结束只是警告，继续停上游。
func TestPauseStopsAtFailureAndWarnsOnLingeringWork(t *testing.T) {
	f := newFixture(t, all...)
	f.apis[Trade].quiesceErr = errors.New("仍有 1 个目标在收敛")
	result, err := f.o.Pause(context.Background(), "")
	require.NoError(t, err)
	require.Len(t, result.Warnings, 1)
	require.Contains(t, result.Warnings[0], "trade")

	g := newFixture(t, all...)
	g.apis[FactorEngine].callErr = errors.New("连接被拒绝")
	result, err = g.o.Pause(context.Background(), "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "暂停 factor-engine")
	require.Equal(t, []string{Strategy, Trade}, result.Paused, "已暂停的保持暂停，不回滚")
}

// 策略的在途操作（目标还没发布到 EventBus）没结束：停在策略这一步，Trade 及更上游都不暂停；
// 策略自己已经暂停（门禁关闭），目标发完后重新执行即可。
func TestPauseStopsAtStrategyWhenItsTargetsAreNotPublished(t *testing.T) {
	f := newFixture(t, all...)
	f.apis[Strategy].quiesceErr = errors.New("还有 2 个目标没有发布到 EventBus")
	result, err := f.o.Pause(context.Background(), "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "暂停 strategy")
	require.Equal(t, []string{Strategy}, result.Paused)
	require.Equal(t, []string{"strategy:pause"}, f.log.snapshot(), "Trade、因子引擎、采集都不能在策略的目标发完之前暂停")
	snap := f.apis[Strategy].c.State()
	require.Equal(t, lifecycle.StagePaused, snap.Stage)

	// 目标发完后重新执行：幂等，继续往上游暂停。
	f.apis[Strategy].quiesceErr = nil
	result, err = f.o.Pause(context.Background(), "")
	require.NoError(t, err)
	require.Equal(t, []string{Strategy, Trade, FactorEngine, Collector}, result.Paused)
}

// 恢复：采集追平后因子才启动，策略与 Trade 一开始就追赶；所有模块追平前谁也不放行；放行顺序上游到下游、Trade 先于策略。
func TestResumeWaitsForUpstreamAndReleasesBehindABarrier(t *testing.T) {
	f := newFixture(t, all...)
	ctx := context.Background()
	_, err := f.o.Pause(ctx, "")
	require.NoError(t, err)
	f.log.mu.Lock()
	f.log.calls = nil
	f.log.mu.Unlock()

	// 每次轮询睡眠时推进一步：先让采集追平，再因子，再策略，最后 Trade。
	steps := []string{Collector, FactorEngine, Strategy, Trade}
	step := 0
	f.o.Sleep = func(ctx context.Context, d time.Duration) error {
		if step < len(steps) {
			*f.caught[steps[step]] = true
			step++
		}
		return ctx.Err()
	}
	result, err := f.o.Resume(ctx, ResumeOptions{})
	require.NoError(t, err)
	require.Equal(t, []string{Collector, FactorEngine, Trade, Strategy}, result.Released, "放行顺序：上游到下游，Trade 先于策略")
	require.Empty(t, result.Pending)

	// 因子在采集追平之后启动；策略、Trade 与采集同时启动（策略要记下因子补算暂停区间时发出的完成事件）。
	require.Less(t, f.index("trade:resume"), f.index("factor-engine:resume"), "Trade 一开始就追赶")
	require.Less(t, f.index("collector:resume"), f.index("factor-engine:resume"))
	require.Less(t, f.index("strategy:resume"), f.index("factor-engine:resume"), "策略一开始就追赶")
	// 采集没有副作用，补齐暂停区间就自己放行（恢复实时采集），不发 release，也不等栅栏。
	require.Equal(t, -1, f.index("collector:release"), "采集自行放行")
	// 栅栏：所有有副作用的模块都 resume 并追平之后才有 release，release 之间按顺序。
	lastResume := max(f.index("factor-engine:resume"), f.index("strategy:resume"), f.index("trade:resume"))
	require.Greater(t, f.index("factor-engine:release"), lastResume)
	require.Less(t, f.index("factor-engine:release"), f.index("trade:release"))
	require.Less(t, f.index("trade:release"), f.index("strategy:release"))
	for _, name := range all {
		snap := f.apis[name].c.State()
		require.Equal(t, lifecycle.StageRunning, snap.Stage, name)
	}
}

// 有模块一直追不平：超时后不放行任何有副作用的模块，报告原因。
func TestResumeTimeoutReleasesNothingByDefault(t *testing.T) {
	f := newFixture(t, Trade, Strategy)
	ctx := context.Background()
	_, err := f.o.Pause(ctx, "")
	require.NoError(t, err)
	*f.caught[Trade] = true // 策略永远追不平
	result, err := f.o.Resume(ctx, ResumeOptions{Timeout: time.Nanosecond})
	require.NoError(t, err)
	require.True(t, result.TimedOut)
	require.Empty(t, result.Released, "超时不自动放行")
	require.Contains(t, result.Pending, Strategy)
	require.Contains(t, result.Pending, Trade)
	require.Equal(t, -1, f.index("trade:release"))
}

// 采集只拉取行情、没有副作用：补齐暂停区间就自己放行，不等其他模块；即使其他模块一直追不平，
// 采集也已经恢复实时采集（否则等待期间的行情又成了新缺口，下游的追赶目标会一直被推远）。
func TestCollectorReleasesItselfWithoutWaitingForTheBarrier(t *testing.T) {
	f := newFixture(t, Collector, Trade)
	ctx := context.Background()
	_, err := f.o.Pause(ctx, "")
	require.NoError(t, err)
	*f.caught[Collector] = true // Trade 一直没追平
	result, err := f.o.Resume(ctx, ResumeOptions{Timeout: time.Nanosecond})
	require.NoError(t, err)
	require.True(t, result.TimedOut)
	require.Equal(t, []string{Collector}, result.Released, "采集已自行放行")
	require.NotContains(t, result.Pending, Collector)
	require.Contains(t, result.Pending, Trade)
	snap := f.apis[Collector].c.State()
	require.Equal(t, lifecycle.StageRunning, snap.Stage)
	require.Equal(t, -1, f.index("collector:release"), "不是栅栏放行的")
	trade := f.apis[Trade].c.State()
	require.Equal(t, lifecycle.StageResuming, trade.Stage)
}

// --release-partial：超时后只放行已追平的模块，未追平的继续追赶。
func TestResumeReleasePartial(t *testing.T) {
	f := newFixture(t, Collector, Trade)
	ctx := context.Background()
	_, err := f.o.Pause(ctx, "")
	require.NoError(t, err)
	*f.caught[Collector] = true
	result, err := f.o.Resume(ctx, ResumeOptions{Timeout: time.Nanosecond, ReleasePartial: true})
	require.NoError(t, err)
	require.True(t, result.TimedOut)
	require.Equal(t, []string{Collector}, result.Released)
	require.Contains(t, result.Pending, Trade)
	snap := f.apis[Trade].c.State()
	require.Equal(t, lifecycle.StageResuming, snap.Stage, "未追平的继续追赶")
}

// 再次执行 resume 是幂等的：已在追赶的模块不重复启动，已放行的不再动；手动暂停的模块不参与。
func TestResumeIsIdempotentAndSkipsManualPause(t *testing.T) {
	f := newFixture(t, Collector, Trade)
	ctx := context.Background()
	_, err := f.o.Pause(ctx, "")
	require.NoError(t, err)
	// Trade 被用户手动暂停（覆盖系统暂停）。
	_, err = f.apis[Trade].c.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdPause, Reason: "我要停"})
	require.NoError(t, err)

	// 第一次：采集没追平，超时退出，采集停在追赶中。
	result, err := f.o.Resume(ctx, ResumeOptions{Timeout: time.Nanosecond})
	require.NoError(t, err)
	require.True(t, result.TimedOut)
	require.Contains(t, result.Pending[Trade], "手动暂停")

	// 第二次：采集追平，只放行采集；不会重复给采集发 resume。
	*f.caught[Collector] = true
	f.log.mu.Lock()
	f.log.calls = nil
	f.log.mu.Unlock()
	result, err = f.o.Resume(ctx, ResumeOptions{})
	require.NoError(t, err)
	require.Equal(t, []string{Collector}, result.Released)
	require.Equal(t, -1, f.index("collector:resume"), "已在追赶，不重复启动")
	require.Equal(t, -1, f.index("trade:resume"), "手动暂停不动")
	snap := f.apis[Trade].c.State()
	require.Equal(t, lifecycle.StagePaused, snap.Stage)
	require.Equal(t, lifecycle.PauseManual, snap.Pause.Kind)

	// 第三次：全部已经不在暂停（采集已放行），没有可做的。
	result, err = f.o.Resume(ctx, ResumeOptions{})
	require.NoError(t, err)
	require.Empty(t, result.Released)
}

func TestStatusReportsEveryModuleAndUnreachableOnes(t *testing.T) {
	f := newFixture(t, Collector, Trade)
	f.apis[Trade].callErr = errors.New("connection refused")
	states := f.o.Status(context.Background())
	require.Len(t, states, 2)
	require.Equal(t, Collector, states[0].Name)
	require.Equal(t, lifecycle.StageRunning, states[0].Stage)
	require.Error(t, states[1].Err)
	var empty Orchestrator
	_, err := empty.Resume(context.Background(), ResumeOptions{})
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "没有可操作的模块"))
}
