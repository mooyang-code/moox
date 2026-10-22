package sysctl

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/stretchr/testify/require"
)

// controllerAPI 把真实的生命周期控制器当作模块的 HTTP 接口用。
type controllerAPI struct {
	c        *lifecycle.Controller
	log      *callLog
	name     string
	statusEr error
	// pauseErr 让 pause 返回错误（模拟在途操作没结束等）。
	pauseErr error
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

func (a *controllerAPI) Status(ctx context.Context) (lifecycle.Snapshot, error) {
	if a.statusEr != nil {
		return lifecycle.Snapshot{}, a.statusEr
	}
	return a.c.Snapshot(), nil
}

func (a *controllerAPI) Exec(ctx context.Context, req lifecycle.CommandRequest) (lifecycle.Object, error) {
	a.log.add(a.name + ":" + string(req.Command))
	obj, err := a.c.Execute(ctx, req.Request())
	if err == nil && req.Command == lifecycle.CmdPause && a.pauseErr != nil {
		return obj, a.pauseErr
	}
	return obj, err
}

func (a *controllerAPI) Catchup(ctx context.Context) (lifecycle.CatchupReport, error) {
	return a.c.CatchupReport(ctx), nil
}

type fixture struct {
	o      *Orchestrator
	log    *callLog
	caught map[string]*bool
	apis   map[string]*controllerAPI
	out    *bytes.Buffer
}

func newFixture(t *testing.T, names ...string) *fixture {
	t.Helper()
	f := &fixture{log: &callLog{}, caught: map[string]*bool{}, apis: map[string]*controllerAPI{}, out: &bytes.Buffer{}}
	var mods []Module
	for _, name := range names {
		flag := new(bool)
		f.caught[name] = flag
		c, err := lifecycle.New(lifecycle.Options{Module: name, Hooks: lifecycle.Hooks{
			Catchup: func(context.Context, lifecycle.Ref, time.Time) (lifecycle.Catchup, error) {
				return lifecycle.Catchup{CaughtUp: *flag, Total: 10}, nil
			},
		}})
		require.NoError(t, err)
		c.Refresh(context.Background())
		api := &controllerAPI{c: c, log: f.log, name: name}
		f.apis[name] = api
		mods = append(mods, Module{Name: name, API: api})
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
		snap, _ := f.apis[name].Status(context.Background())
		require.Equal(t, lifecycle.StagePaused, snap.Self.Stage, name)
		require.Equal(t, lifecycle.PauseSystem, snap.Self.Pause.Kind, name)
		require.Equal(t, "切换行情源", snap.Self.Pause.Reason)
		require.Equal(t, "test", snap.Self.Pause.By)
	}
	// 幂等：再暂停一次不报错。
	_, err = f.o.Pause(context.Background(), "切换行情源")
	require.NoError(t, err)
}

// 某个模块暂停失败：停在那里，不回滚已暂停的部分；在途操作没结束只是警告，继续停上游。
func TestPauseStopsAtFailureAndWarnsOnLingeringWork(t *testing.T) {
	f := newFixture(t, all...)
	f.apis[Trade].pauseErr = errors.New("已暂停，但在途操作没有结束：仍有 1 个目标在收敛")
	result, err := f.o.Pause(context.Background(), "")
	require.NoError(t, err)
	require.Len(t, result.Warnings, 1)
	require.Contains(t, result.Warnings[0], "trade")

	g := newFixture(t, all...)
	g.apis[FactorEngine].pauseErr = errors.New("连接被拒绝")
	result, err = g.o.Pause(context.Background(), "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "暂停 factor-engine")
	require.Equal(t, []string{Strategy, Trade}, result.Paused, "已暂停的保持暂停，不回滚")
}

// 恢复：上游追平后才启动下游；所有模块追平前谁也不放行；放行顺序上游到下游、Trade 先于策略。
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

	// 下游在上游追平后才启动：因子在采集追平之后，策略在因子追平之后；Trade 与采集同时启动。
	require.Less(t, f.index("trade:resume"), f.index("factor-engine:resume"), "Trade 一开始就追赶")
	require.Less(t, f.index("collector:resume"), f.index("factor-engine:resume"))
	require.Less(t, f.index("factor-engine:resume"), f.index("strategy:resume"))
	// 栅栏：所有 resume 之后才有 release，release 之间按顺序。
	lastResume := max(f.index("collector:resume"), f.index("factor-engine:resume"), f.index("strategy:resume"), f.index("trade:resume"))
	require.Greater(t, f.index("collector:release"), lastResume)
	require.Less(t, f.index("collector:release"), f.index("factor-engine:release"))
	require.Less(t, f.index("factor-engine:release"), f.index("trade:release"))
	require.Less(t, f.index("trade:release"), f.index("strategy:release"))
	for _, name := range all {
		snap, _ := f.apis[name].Status(ctx)
		require.Equal(t, lifecycle.StageReady, snap.Self.Stage, name)
	}
}

// 有模块一直追不平：超时后不放行任何模块，报告原因。
func TestResumeTimeoutReleasesNothingByDefault(t *testing.T) {
	f := newFixture(t, Collector, Strategy)
	ctx := context.Background()
	_, err := f.o.Pause(ctx, "")
	require.NoError(t, err)
	*f.caught[Collector] = true // 策略永远追不平（它依赖因子，这里没有因子模块：直接按自己的进度）
	result, err := f.o.Resume(ctx, ResumeOptions{Timeout: time.Nanosecond})
	require.NoError(t, err)
	require.True(t, result.TimedOut)
	require.Empty(t, result.Released, "超时不自动放行")
	require.Contains(t, result.Pending, Strategy)
	require.Contains(t, result.Pending, Collector)
	require.Zero(t, f.index("collector:release")+1)
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
	snap, _ := f.apis[Trade].Status(ctx)
	require.Equal(t, lifecycle.StageResuming, snap.Self.Stage, "未追平的继续追赶")
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
	snap, _ := f.apis[Trade].Status(ctx)
	require.Equal(t, lifecycle.StagePaused, snap.Self.Stage)
	require.Equal(t, lifecycle.PauseManual, snap.Self.Pause.Kind)

	// 第三次：全部已经不在暂停（采集已放行），没有可做的。
	result, err = f.o.Resume(ctx, ResumeOptions{})
	require.NoError(t, err)
	require.Empty(t, result.Released)
}

func TestStatusReportsEveryModuleAndUnreachableOnes(t *testing.T) {
	f := newFixture(t, Collector, Trade)
	f.apis[Trade].statusEr = errors.New("connection refused")
	states := f.o.Status(context.Background())
	require.Len(t, states, 2)
	require.Equal(t, Collector, states[0].Name)
	require.Equal(t, lifecycle.StageReady, states[0].Stage)
	require.Error(t, states[1].Err)
	var empty Orchestrator
	_, err := empty.Resume(context.Background(), ResumeOptions{})
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "没有配置"))
}
