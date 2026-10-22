package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// catchupFake 让测试控制各对象的追赶进度。
type catchupFake struct {
	mu     sync.Mutex
	caught map[string]bool
	calls  int
}

func newCatchupFake() *catchupFake { return &catchupFake{caught: map[string]bool{}} }

func (f *catchupFake) set(ref Ref, caught bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.caught[ref.key()] = caught
}

func (f *catchupFake) hook(_ context.Context, ref Ref, since time.Time) (Catchup, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	caught := f.caught[ref.key()]
	cu := Catchup{Total: 10, CaughtUp: caught}
	if caught {
		cu.Done = 10
	}
	return cu, nil
}

var set2 = Ref{Kind: "factor_set", ID: "set2"}

// fixture 是两个运行中的对象加可控的追赶钩子。
type fixture struct {
	t    *testing.T
	f    *fakeModule
	cu   *catchupFake
	c    *Controller
	file string
}

func newFixture(t *testing.T, file string) *fixture {
	t.Helper()
	f := newFake()
	f.refs = []Ref{set1, set2}
	f.setObject(set1, pass("ok"))
	f.setObject(set2, pass("ok"))
	cu := newCatchupFake()
	hooks := f.hooks()
	hooks.Catchup = cu.hook
	now := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	c, err := New(Options{Module: "factor-engine", StateFile: file, Now: func() time.Time { return now }, Hooks: hooks})
	require.NoError(t, err)
	fx := &fixture{t: t, f: f, cu: cu, c: c, file: file}
	ctx := context.Background()
	c.Refresh(ctx)
	for _, r := range []Ref{set1, set2} {
		for _, cmd := range []Command{CmdInit, CmdStart, CmdConfirm} {
			_, err := c.Command(ctx, cmd, r)
			require.NoError(t, err)
		}
	}
	return fx
}

func TestPauseFromReadyAndKindPriority(t *testing.T) {
	f := newFake()
	f.refs = []Ref{set1}
	f.setObject(set1, pass("ok"))
	c := newController(t, f, "")
	ctx := context.Background()
	c.Refresh(ctx)
	_, err := c.Command(ctx, CmdInit, set1)
	require.NoError(t, err)

	obj, err := c.Execute(ctx, Request{Command: CmdPause, Ref: set1, PauseKind: PauseSystem, Reason: "切换行情源", By: "cli:ops"})
	require.NoError(t, err, "就绪的对象也能暂停")
	require.Equal(t, StagePaused, obj.Stage)
	require.Equal(t, StageReady, obj.Resume)
	require.Equal(t, PauseInfo{Kind: PauseSystem, Reason: "切换行情源", By: "cli:ops", Since: obj.Pause.Since}, *obj.Pause)
	require.Equal(t, "系统暂停：切换行情源", obj.Reason)

	// 手动暂停覆盖系统暂停；自动暂停不覆盖手动暂停。
	obj, err = c.Execute(ctx, Request{Command: CmdPause, Ref: set1, Reason: "我要停"})
	require.NoError(t, err)
	require.Equal(t, PauseManual, obj.Pause.Kind)
	obj, err = c.Execute(ctx, Request{Command: CmdPause, Ref: set1, PauseKind: PauseSystem})
	require.NoError(t, err)
	require.Equal(t, PauseManual, obj.Pause.Kind, "系统暂停不覆盖手动暂停")

	// 系统恢复只恢复系统暂停：手动暂停原样不动。
	obj, err = c.Execute(ctx, Request{Command: CmdResume, Ref: set1, PauseKind: PauseSystem})
	require.NoError(t, err)
	require.Equal(t, StagePaused, obj.Stage)
	obj, err = c.Execute(ctx, Request{Command: CmdResume, Ref: set1})
	require.NoError(t, err)
	require.Equal(t, StageReady, obj.Stage, "手动恢复回到暂停前的就绪")
	require.Nil(t, obj.Pause)
	require.True(t, obj.PausedSince.IsZero())

	_, err = c.Execute(ctx, Request{Command: CmdPause, Ref: set1, PauseKind: "bogus"})
	var denied *ErrNotAllowed
	require.ErrorAs(t, err, &denied)
}

// 恢复是两段式：追赶期间不计算，追平后自动放行回到暂停前的阶段。
func TestResumeCatchesUpBeforeRelease(t *testing.T) {
	fx := newFixture(t, "")
	ctx := context.Background()
	_, err := fx.c.Command(ctx, CmdPause, set1)
	require.NoError(t, err)
	require.False(t, fx.c.Allowed(set1))
	require.False(t, fx.c.CatchupAllowed(set1), "暂停期间追赶门禁也关")

	obj, err := fx.c.Command(ctx, CmdResume, set1)
	require.NoError(t, err)
	require.Equal(t, StageResuming, obj.Stage, "没追平：停在恢复追赶")
	require.Equal(t, StageRunning, obj.Resume)
	require.False(t, fx.c.Allowed(set1), "追赶期间不做实时计算")
	require.True(t, fx.c.CatchupAllowed(set1), "单个对象追赶：模块在运行，对象在恢复追赶")
	require.False(t, fx.c.CatchupAllowed(set2), "别的对象没在追赶")
	require.True(t, fx.c.Allowed(set2), "别的对象不受影响")
	require.NotNil(t, obj.Catchup)
	require.False(t, obj.Catchup.CaughtUp)
	require.Equal(t, int64(10), obj.Catchup.Total)

	fx.c.Refresh(ctx)
	require.Equal(t, StageResuming, fx.c.Stage(set1), "没追平不会放行")

	fx.cu.set(set1, true)
	fx.c.Refresh(ctx)
	require.Equal(t, StageRunning, fx.c.Stage(set1), "追平后自动放行")
	require.True(t, fx.c.Allowed(set1))
	require.False(t, fx.c.CatchupAllowed(set1))
}

// 系统级恢复：追平后停在待放行，等 release；没追平的 release 被拒绝。
func TestManualReleaseWaitsForBarrier(t *testing.T) {
	fx := newFixture(t, "")
	ctx := context.Background()
	_, err := fx.c.Execute(ctx, Request{Command: CmdPause, Ref: set1, PauseKind: PauseSystem})
	require.NoError(t, err)
	_, err = fx.c.Execute(ctx, Request{Command: CmdResume, Ref: set1, PauseKind: PauseSystem, ManualRelease: true})
	require.NoError(t, err)

	var denied *ErrNotAllowed
	_, err = fx.c.Command(ctx, CmdRelease, set1)
	require.ErrorAs(t, err, &denied, "没追平不能放行")

	fx.cu.set(set1, true)
	fx.c.Refresh(ctx)
	require.Equal(t, StageResuming, fx.c.Stage(set1), "追平但设为手动放行：停在待放行")
	rep := fx.c.CatchupReport(ctx)
	require.True(t, rep.Resuming)
	require.True(t, rep.CaughtUp)

	obj, err := fx.c.Command(ctx, CmdRelease, set1)
	require.NoError(t, err)
	require.Equal(t, StageRunning, obj.Stage)
	require.True(t, fx.c.Allowed(set1))

	_, err = fx.c.Command(ctx, CmdRelease, set1)
	require.ErrorAs(t, err, &denied, "已经放行")
}

// 放行前再核一次：待放行期间又出现缺口（追赶进度回退），release 被拒绝。
func TestReleaseRechecksCatchup(t *testing.T) {
	fx := newFixture(t, "")
	ctx := context.Background()
	_, _ = fx.c.Command(ctx, CmdPause, set1)
	_, err := fx.c.Execute(ctx, Request{Command: CmdResume, Ref: set1, ManualRelease: true})
	require.NoError(t, err)
	fx.cu.set(set1, true)
	fx.c.Refresh(ctx)
	fx.cu.set(set1, false) // 追赶目标前移，又出现缺口
	var denied *ErrNotAllowed
	_, err = fx.c.Command(ctx, CmdRelease, set1)
	require.ErrorAs(t, err, &denied)
	require.Equal(t, StageResuming, fx.c.Stage(set1))
}

// 模块级恢复：参与本次恢复的是暂停前活动的对象；手动暂停的对象继续排除；全部追平后模块才放行。
func TestModuleResumeMembersAndManualExclusion(t *testing.T) {
	fx := newFixture(t, "")
	ctx := context.Background()
	_, err := fx.c.Command(ctx, CmdPause, set2) // 手动暂停 set2
	require.NoError(t, err)
	_, err = fx.c.Execute(ctx, Request{Command: CmdPause, PauseKind: PauseSystem, Reason: "切换行情源"}) // 模块系统暂停
	require.NoError(t, err)
	require.False(t, fx.c.Allowed(set1))
	snap := fx.c.Snapshot()
	require.Equal(t, StagePaused, snap.Stage)
	require.Equal(t, StagePaused, snap.Objects[0].Effective, "对象自身仍是运行，有效阶段是暂停")
	require.Equal(t, StageRunning, snap.Objects[0].Stage)
	require.False(t, snap.Degraded, "系统暂停不算降级")

	obj, err := fx.c.Execute(ctx, Request{Command: CmdResume, PauseKind: PauseSystem, ManualRelease: true})
	require.NoError(t, err)
	require.Equal(t, StageResuming, obj.Stage)
	require.True(t, fx.c.CatchupAllowed(Ref{}))
	require.True(t, fx.c.CatchupAllowed(set1), "原活动对象参与追赶")
	require.False(t, fx.c.CatchupAllowed(set2), "手动暂停的对象排除")
	require.False(t, fx.c.Allowed(set1), "追赶期间不做实时计算")

	fx.c.Refresh(ctx)
	rep := fx.c.CatchupReport(ctx)
	require.False(t, rep.CaughtUp)
	require.Equal(t, "factor_set/set1", rep.Self.Catchup.BlockedBy, "被没追平的参与对象阻塞")

	fx.cu.set(Ref{}, true)
	fx.cu.set(set1, true)
	fx.c.Refresh(ctx)
	require.True(t, fx.c.CatchupReport(ctx).CaughtUp)
	obj, err = fx.c.Command(ctx, CmdRelease, Ref{})
	require.NoError(t, err)
	require.Equal(t, StageReady, obj.Stage, "模块自身暂停前是就绪")
	require.Equal(t, HoldNone, obj.Hold)
	require.True(t, fx.c.Allowed(set1))
	require.False(t, fx.c.Allowed(set2), "手动暂停的对象仍然停着")
	require.Equal(t, StagePaused, fx.c.Stage(set2))
}

func TestPauseDuringCatchupKeepsEarliestPausedSince(t *testing.T) {
	fx := newFixture(t, "")
	ctx := context.Background()
	t0 := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	obj, err := fx.c.Command(ctx, CmdPause, set1)
	require.NoError(t, err)
	require.Equal(t, t0, obj.PausedSince)
	_, err = fx.c.Command(ctx, CmdResume, set1)
	require.NoError(t, err)
	obj, err = fx.c.Execute(ctx, Request{Command: CmdPause, Ref: set1, Reason: "追赶太慢"})
	require.NoError(t, err)
	require.Equal(t, StagePaused, obj.Stage)
	require.Equal(t, t0, obj.PausedSince, "暂停起点保持最早的那次")
	require.Equal(t, StageRunning, obj.Resume)
	require.Nil(t, obj.Catchup)
	require.False(t, fx.c.CatchupAllowed(set1))
}

func TestObjectResumeNeedsForceWhileModulePaused(t *testing.T) {
	fx := newFixture(t, "")
	ctx := context.Background()
	_, err := fx.c.Command(ctx, CmdPause, set1)
	require.NoError(t, err)
	_, err = fx.c.Execute(ctx, Request{Command: CmdPause, PauseKind: PauseSystem})
	require.NoError(t, err)

	var denied *ErrNotAllowed
	_, err = fx.c.Command(ctx, CmdResume, set1)
	require.ErrorAs(t, err, &denied, "系统暂停期间单个对象不能恢复")

	obj, err := fx.c.Execute(ctx, Request{Command: CmdResume, Ref: set1, Force: true})
	require.NoError(t, err)
	require.Equal(t, StageResuming, obj.Stage)
	require.True(t, fx.c.CatchupAllowed(set1), "force 只放行这个对象的追赶")
	require.False(t, fx.c.CatchupAllowed(set2), "不放开整个模块")
	require.False(t, fx.c.Allowed(set1), "模块仍在暂停，实时计算依旧关闭")
}

// 恢复追赶是进程内的任务：重启后回到暂停，暂停起点不变。
func TestRestartDuringCatchupReturnsToPaused(t *testing.T) {
	file := filepath.Join(t.TempDir(), "lifecycle.json")
	fx := newFixture(t, file)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	_, _ = fx.c.Command(ctx, CmdPause, set1)
	_, err := fx.c.Command(ctx, CmdResume, set1)
	require.NoError(t, err)
	require.Equal(t, StageResuming, fx.c.Stage(set1))

	hooks := fx.f.hooks()
	hooks.Catchup = fx.cu.hook
	c2, err := New(Options{Module: "factor-engine", StateFile: file, Now: func() time.Time { return t0.Add(time.Hour) }, Hooks: hooks})
	require.NoError(t, err)
	c2.Refresh(ctx)
	require.Equal(t, StagePaused, c2.Stage(set1), "重启后恢复追赶回到暂停")
	snap := c2.Snapshot()
	var got Object
	for _, o := range snap.Objects {
		if o.Ref == set1 {
			got = o
		}
	}
	require.Equal(t, t0, got.PausedSince, "暂停起点不丢")
	require.NotNil(t, got.Pause)
	require.Nil(t, got.Catchup)
}

func TestSystemPauseHoldWorksEvenWhenModuleNotReady(t *testing.T) {
	f := newFake()
	f.module = []Check{fail("依赖", "Storage 不可达")}
	f.refs = []Ref{set1}
	f.setObject(set1, pass("ok"))
	c := newController(t, f, "")
	ctx := context.Background()
	c.Refresh(ctx)
	require.Equal(t, StageNotReady, c.Stage(Ref{}))

	obj, err := c.Execute(ctx, Request{Command: CmdPause, PauseKind: PauseSystem})
	require.NoError(t, err, "模块自身未就绪也能建立系统暂停")
	require.Equal(t, HoldSystemPause, obj.Hold)
	require.False(t, c.Allowed(set1))

	f.mu.Lock()
	f.module = nil
	f.mu.Unlock()
	c.Refresh(ctx)
	require.Equal(t, StageReady, c.Stage(Ref{}))
	require.False(t, c.Allowed(set1), "模块恢复就绪后，hold 仍然关着门禁")
	require.Equal(t, StagePaused, c.Snapshot().Stage)

	obj, err = c.Execute(ctx, Request{Command: CmdResume, PauseKind: PauseSystem})
	require.NoError(t, err)
	require.Equal(t, HoldNone, obj.Hold)
}

func TestPostResetHoldBlocksAutoInit(t *testing.T) {
	f := newFake()
	f.refs = []Ref{set1}
	f.setObject(set1, pass("ok"))
	c := newController(t, f, "")
	ctx := context.Background()
	c.mu.Lock()
	c.self.Hold = HoldPostReset
	c.mu.Unlock()
	c.Refresh(ctx)
	require.Equal(t, StageUninitialized, c.Stage(Ref{}), "重置后 Refresh 不自动初始化模块自身")
	require.False(t, c.CatchupAllowed(set1))

	obj, err := c.Command(ctx, CmdInit, Ref{})
	require.NoError(t, err)
	require.Equal(t, StageReady, obj.Stage)
	require.Equal(t, HoldNone, obj.Hold)
}

// 门禁矩阵：自身未初始化、未就绪、重置、故障时一律不放行实时计算与追赶。
func TestGateMatrixDeniesNonOperationalModuleStages(t *testing.T) {
	fx := newFixture(t, "")
	for _, stage := range []Stage{StageUninitialized, StageNotReady, StageResetting, StageResetFailed, StageFault} {
		fx.c.mu.Lock()
		fx.c.self.Stage = stage
		fx.c.objects[set1.key()].Stage = StageResuming // 即使对象在追赶
		fx.c.mu.Unlock()
		require.False(t, fx.c.Allowed(set2), stage)
		require.False(t, fx.c.CatchupAllowed(set1), stage)
		fx.c.mu.Lock()
		fx.c.objects[set1.key()].Stage = StageRunning
		fx.c.mu.Unlock()
	}
}

// 手动和系统暂停是用户意图，不算降级；自动暂停算降级。
func TestDegradedOnlyForNonIntentionalPause(t *testing.T) {
	fx := newFixture(t, "")
	ctx := context.Background()
	_, err := fx.c.Command(ctx, CmdPause, set1)
	require.NoError(t, err)
	snap := fx.c.Snapshot()
	require.False(t, snap.Degraded)
	require.Equal(t, StageRunning, snap.Stage, "另一个对象在跑，汇总取在跑的阶段")

	fx.f.setObject(set2, fail("数据源", "断档"))
	fx.c.Refresh(ctx)
	snap = fx.c.Snapshot()
	require.True(t, snap.Degraded, "自动暂停算降级")
	require.Equal(t, StagePaused, snap.Stage, "没有对象在跑，汇总是暂停")

	compact := fx.c.Compact()
	require.Equal(t, 1, compact.PauseKinds[PauseManual])
	require.Equal(t, 1, compact.PauseKinds[PauseAuto])
	require.Len(t, compact.Attention, 2)
}

// 自动暂停后检查恢复，也走追赶 → 放行这条路径，没有缺口时在同一次 Refresh 内完成。
func TestAutoResumeGoesThroughCatchup(t *testing.T) {
	fx := newFixture(t, "")
	ctx := context.Background()
	fx.f.setObject(set1, fail("数据源", "断档"))
	fx.c.Refresh(ctx)
	require.Equal(t, StagePaused, fx.c.Stage(set1))

	fx.f.setObject(set1, pass("ok"))
	fx.c.Refresh(ctx)
	require.Equal(t, StageResuming, fx.c.Stage(set1), "检查恢复后进入追赶，没追平不放行")

	fx.cu.set(set1, true)
	fx.c.Refresh(ctx)
	require.Equal(t, StageRunning, fx.c.Stage(set1))
	fx.f.mu.Lock()
	defer fx.f.mu.Unlock()
	require.Contains(t, fx.f.changes, "factor_set/set1:paused>resuming")
	require.Contains(t, fx.f.changes, "factor_set/set1:resuming>running")
}

func TestStageColorsOfNewStages(t *testing.T) {
	require.Equal(t, "cyan", StageResuming.Color())
	require.Equal(t, "purple", StageResetting.Color())
	require.Equal(t, "red", StageResetFailed.Color())
	require.False(t, StageResuming.Active())
}

func TestHTTPPauseKindAndCatchup(t *testing.T) {
	fx := newFixture(t, "")
	mux := http.NewServeMux()
	mux.Handle(CommandPath, fx.c.CommandHandler())
	mux.Handle(CatchupPath, fx.c.CatchupHandler())
	post := func(body any) (int, Object) {
		raw, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, CommandPath, bytes.NewReader(raw)))
		var obj Object
		_ = json.Unmarshal(rec.Body.Bytes(), &obj)
		return rec.Code, obj
	}
	code, obj := post(CommandRequest{Command: CmdPause, Kind: set1.Kind, ID: set1.ID, PauseKind: PauseSystem, Reason: "测试", By: "cli:ops"})
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, PauseSystem, obj.Pause.Kind)
	require.Equal(t, "cli:ops", obj.Pause.By)

	code, obj = post(CommandRequest{Command: CmdResume, Kind: set1.Kind, ID: set1.ID, ManualRelease: true})
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, StageResuming, obj.Stage)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, CatchupPath, nil))
	var rep CatchupReport
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rep))
	require.True(t, rep.Resuming)
	require.False(t, rep.CaughtUp)
	require.Len(t, rep.Objects, 1)

	code, _ = post(CommandRequest{Command: CmdRelease, Kind: set1.Kind, ID: set1.ID})
	require.Equal(t, http.StatusConflict, code, "没追平不能放行")
}

// Halted 只在有意停下时为真：自身未就绪（检查抖动）不算，暂停、恢复追赶、重置、故障才算。
func TestHaltedOnlyForDeliberateStops(t *testing.T) {
	f := newFake()
	f.module = []Check{fail("依赖", "EventBus 短暂不可用")}
	c := newController(t, f, "")
	ctx := context.Background()
	c.Refresh(ctx)
	require.Equal(t, StageNotReady, c.Stage(Ref{}))
	require.False(t, c.Halted(Ref{}), "检查失败的未就绪不算有意停下")

	f.mu.Lock()
	f.module = nil
	f.mu.Unlock()
	c.Refresh(ctx)
	require.False(t, c.Halted(Ref{}))
	_, err := c.Execute(ctx, Request{Command: CmdPause, Reason: "维护"})
	require.NoError(t, err)
	require.True(t, c.Halted(Ref{}))
	_, err = c.Command(ctx, CmdResume, Ref{})
	require.NoError(t, err)
	require.False(t, c.Halted(Ref{}))

	_, err = c.Execute(ctx, Request{Command: CmdPause, PauseKind: PauseSystem})
	require.NoError(t, err)
	c.mu.Lock()
	c.self.Stage = StageResetting
	c.mu.Unlock()
	require.True(t, c.Halted(Ref{}))
}

// 暂停后调用 Quiesce 等在途操作结束；没结束时命令返回错误但对象已经暂停。
func TestPauseCallsQuiesceAfterGateCloses(t *testing.T) {
	f := newFake()
	var stageSeen Stage
	var c *Controller
	hooks := f.hooks()
	hooks.Quiesce = func(_ context.Context, ref Ref) error {
		stageSeen = c.Stage(ref)
		return errors.New("还有 2 个订单请求在途")
	}
	c, err := New(Options{Module: "trade", Hooks: hooks})
	require.NoError(t, err)
	ctx := context.Background()
	c.Refresh(ctx)

	obj, err := c.Command(ctx, CmdPause, Ref{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "已暂停，但在途操作没有结束")
	require.Equal(t, StagePaused, obj.Stage)
	require.Equal(t, StagePaused, stageSeen, "调用 Quiesce 时门禁已经关闭")
	require.True(t, c.Halted(Ref{}))
}

// 模块恢复追赶时，参与追赶的对象没有自己的暂停时间：钩子拿到的追赶起点取模块的暂停时间。
func TestModuleCatchupGivesMembersTheModulePausedSince(t *testing.T) {
	f := newFake()
	f.refs = []Ref{set1}
	f.setObject(set1, pass("ok"))
	var gotSince []time.Time
	hooks := f.hooks()
	hooks.Catchup = func(_ context.Context, ref Ref, since time.Time) (Catchup, error) {
		if ref == set1 {
			gotSince = append(gotSince, since)
		}
		return Catchup{CaughtUp: false}, nil
	}
	now := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	c, err := New(Options{Module: "m", Now: func() time.Time { return now }, Hooks: hooks})
	require.NoError(t, err)
	ctx := context.Background()
	c.Refresh(ctx)
	for _, cmd := range []Command{CmdInit, CmdStart, CmdConfirm} {
		_, err = c.Command(ctx, cmd, set1)
		require.NoError(t, err)
	}
	_, err = c.Command(ctx, CmdPause, Ref{})
	require.NoError(t, err)
	_, err = c.Command(ctx, CmdResume, Ref{})
	require.NoError(t, err)
	require.NotEmpty(t, gotSince)
	for _, since := range gotSince {
		require.Equal(t, now, since, "对象的追赶起点是模块的暂停时间，不是零值")
	}
}

// 定时恢复：暂停时带到期时间，到点后下一轮 Refresh 自动进入恢复追赶，追平后放行；系统暂停与过去的时间被拒绝。
func TestPauseExpiresAutoResumes(t *testing.T) {
	f := newFake()
	f.refs = []Ref{set1}
	f.setObject(set1, pass("ok"))
	cu := newCatchupFake()
	hooks := f.hooks()
	hooks.Catchup = cu.hook
	now := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	c, err := New(Options{Module: "factor-engine", Now: func() time.Time { return now }, Hooks: hooks})
	require.NoError(t, err)
	ctx := context.Background()
	c.Refresh(ctx)
	for _, cmd := range []Command{CmdInit, CmdStart, CmdConfirm} {
		_, err := c.Command(ctx, cmd, set1)
		require.NoError(t, err)
	}

	_, err = c.Execute(ctx, Request{Command: CmdPause, Ref: set1, PauseKind: PauseSystem, ExpiresAt: now.Add(time.Hour)})
	require.ErrorContains(t, err, "系统暂停不支持定时恢复")
	_, err = c.Execute(ctx, Request{Command: CmdPause, Ref: set1, ExpiresAt: now.Add(-time.Minute)})
	require.ErrorContains(t, err, "晚于现在")

	obj, err := c.Execute(ctx, Request{Command: CmdPause, Ref: set1, Reason: "夜里停一会", ExpiresAt: now.Add(2 * time.Hour)})
	require.NoError(t, err)
	require.Equal(t, now.Add(2*time.Hour), obj.Pause.ExpiresAt)

	now = now.Add(time.Hour)
	c.Refresh(ctx)
	require.Equal(t, StagePaused, c.Stage(set1), "没到点不恢复")

	now = now.Add(time.Hour)
	c.Refresh(ctx)
	require.Equal(t, StageResuming, c.Stage(set1), "到点进入恢复追赶，不是直接放行")
	cu.set(set1, true)
	c.Refresh(ctx)
	require.True(t, c.Allowed(set1), "追平后自动放行")
}

// 没有到期时间的命令请求不带 expires_at：旧版本模块按已知字段严格解析请求，多一个字段就返回 400（线上 Monitor 就因此拒绝过新版命令行）。
func TestCommandRequestOmitsZeroExpiry(t *testing.T) {
	raw, err := json.Marshal(CommandRequest{Command: CmdPause, PauseKind: PauseSystem, By: "ops"})
	require.NoError(t, err)
	require.NotContains(t, string(raw), "expires_at")
	raw, err = json.Marshal(CommandRequest{Command: CmdPause, ExpiresAt: time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)})
	require.NoError(t, err)
	require.Contains(t, string(raw), "expires_at")
}
