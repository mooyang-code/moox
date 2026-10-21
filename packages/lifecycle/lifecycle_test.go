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

// fakeModule 是被测控制器的钩子：检查结果可以被测试随时改变。
type fakeModule struct {
	mu       sync.Mutex
	module   []Check
	objects  map[string][]Check
	refs     []Ref
	inits    []Ref
	changes  []string
	initFail error
}

func newFake() *fakeModule {
	return &fakeModule{objects: map[string][]Check{}}
}

func (f *fakeModule) hooks() Hooks {
	return Hooks{
		CheckModule: func(context.Context) []Check { f.mu.Lock(); defer f.mu.Unlock(); return f.module },
		CheckObject: func(_ context.Context, r Ref) []Check { f.mu.Lock(); defer f.mu.Unlock(); return f.objects[r.key()] },
		Objects:     func() []Ref { f.mu.Lock(); defer f.mu.Unlock(); return append([]Ref(nil), f.refs...) },
		Init: func(_ context.Context, r Ref) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.inits = append(f.inits, r)
			return f.initFail
		},
		OnChange: func(r Ref, from, to Stage, reason string) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.changes = append(f.changes, r.key()+":"+string(from)+">"+string(to))
		},
	}
}

func (f *fakeModule) setObject(ref Ref, checks ...Check) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[ref.key()] = checks
}

func fail(name, msg string) Check {
	return Check{Name: name, Status: CheckFail, Message: msg, Remedy: "修复它"}
}
func pass(name string) Check { return Check{Name: name, Status: CheckPass} }

var set1 = Ref{Kind: "factor_set", ID: "set1"}

func newController(t *testing.T, f *fakeModule, file string) *Controller {
	t.Helper()
	now := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	c, err := New(Options{Module: "factor-engine", StateFile: file, Now: func() time.Time { return now }, Hooks: f.hooks()})
	require.NoError(t, err)
	return c
}

func TestStageColorsAndActive(t *testing.T) {
	want := map[Stage]string{StageUninitialized: "gray", StageNotReady: "red", StageReady: "blue", StageWarming: "yellow", StageRunning: "green", StagePaused: "orange", StageFault: "red"}
	for stage, color := range want {
		require.Equal(t, color, stage.Color(), stage)
	}
	require.True(t, StageWarming.Active())
	require.True(t, StageRunning.Active())
	require.False(t, StageReady.Active())
	require.False(t, StagePaused.Active())
}

// 新对象是未初始化；init 之后检查通过变就绪、不通过变未就绪；就绪不会自动进入预热。
func TestInitCheckAndNeverAutoStart(t *testing.T) {
	f := newFake()
	f.refs = []Ref{set1}
	f.setObject(set1, fail("本地数据", "标的 BTC 缺 5000 根"))
	c := newController(t, f, "")
	ctx := context.Background()
	c.Refresh(ctx)
	require.Equal(t, StageUninitialized, c.Stage(set1), "没有 init 之前不检查")

	obj, err := c.Command(ctx, CmdInit, set1)
	require.NoError(t, err)
	require.Equal(t, StageNotReady, obj.Stage)
	require.Contains(t, obj.Reason, "本地数据")
	require.Equal(t, "FAIL", string(obj.Checks[0].Status))
	require.Equal(t, "修复它", obj.Checks[0].Remedy)

	f.setObject(set1, pass("本地数据"))
	c.Refresh(ctx)
	require.Equal(t, StageReady, c.Stage(set1))
	c.Refresh(ctx)
	require.Equal(t, StageReady, c.Stage(set1), "就绪不会自动进入预热")

	// 就绪之后检查又失败：退回未就绪。
	f.setObject(set1, fail("本地数据", "变更日志断档"))
	c.Refresh(ctx)
	require.Equal(t, StageNotReady, c.Stage(set1))
}

func TestStartConfirmPauseResumeFlow(t *testing.T) {
	f := newFake()
	f.refs = []Ref{set1}
	f.setObject(set1, pass("ok"))
	c := newController(t, f, "")
	ctx := context.Background()
	_, err := c.Command(ctx, CmdInit, set1)
	require.NoError(t, err)

	var denied *ErrNotAllowed
	_, err = c.Command(ctx, CmdConfirm, set1)
	require.ErrorAs(t, err, &denied, "没有预热不能确认")

	obj, err := c.Command(ctx, CmdStart, set1)
	require.NoError(t, err)
	require.Equal(t, StageWarming, obj.Stage)
	require.True(t, c.Allowed(set1))
	require.True(t, c.Warming(set1))

	_, err = c.Command(ctx, CmdStart, set1)
	require.ErrorAs(t, err, &denied, "已经在预热")

	obj, err = c.Command(ctx, CmdConfirm, set1)
	require.NoError(t, err)
	require.Equal(t, StageRunning, obj.Stage)
	require.False(t, c.Warming(set1))

	obj, err = c.Command(ctx, CmdPause, set1)
	require.NoError(t, err)
	require.Equal(t, StagePaused, obj.Stage)
	require.Equal(t, "手动暂停", obj.Reason)
	require.False(t, c.Allowed(set1))
	c.Refresh(ctx)
	require.Equal(t, StagePaused, c.Stage(set1), "手动暂停不会被检查通过自动恢复")

	obj, err = c.Command(ctx, CmdResume, set1)
	require.NoError(t, err)
	require.Equal(t, StageRunning, obj.Stage, "恢复到暂停前的阶段")

	// 检查失败时不能启动 / 确认 / 恢复。
	f.setObject(set1, fail("上游", "断档"))
	_, err = c.Command(ctx, CmdPause, set1)
	require.NoError(t, err)
	_, err = c.Command(ctx, CmdResume, set1)
	require.ErrorAs(t, err, &denied)
	require.Contains(t, err.Error(), "断档")
}

// 运行中检查失败只暂停受影响的对象；通过之后自动回到暂停前的阶段；别的对象不受影响。
func TestAutoPauseOnlyAffectedObjectAndAutoResume(t *testing.T) {
	set2 := Ref{Kind: "factor_set", ID: "set2"}
	f := newFake()
	f.refs = []Ref{set1, set2}
	f.setObject(set1, pass("ok"))
	f.setObject(set2, pass("ok"))
	c := newController(t, f, "")
	ctx := context.Background()
	for _, r := range []Ref{set1, set2} {
		_, err := c.Command(ctx, CmdInit, r)
		require.NoError(t, err)
		_, err = c.Command(ctx, CmdStart, r)
		require.NoError(t, err)
	}
	_, err := c.Command(ctx, CmdConfirm, set1)
	require.NoError(t, err) // set1 运行，set2 预热

	f.setObject(set1, fail("数据源", "变更日志断档"))
	c.Refresh(ctx)
	require.Equal(t, StagePaused, c.Stage(set1))
	require.Equal(t, StageWarming, c.Stage(set2), "别的对象照常")
	snap := c.Snapshot()
	require.True(t, snap.Objects[0].AutoPaused)
	require.Equal(t, StageRunning, snap.Objects[0].Resume)
	require.Contains(t, snap.Objects[0].Reason, "变更日志断档")
	require.True(t, snap.Degraded)

	f.setObject(set1, pass("ok"))
	c.Refresh(ctx)
	require.Equal(t, StageRunning, c.Stage(set1), "检查恢复后自动回到暂停前的阶段")
	require.False(t, c.Snapshot().Degraded)
}

func TestFaultNeedsManualResume(t *testing.T) {
	f := newFake()
	f.refs = []Ref{set1}
	f.setObject(set1, pass("ok"))
	c := newController(t, f, "")
	ctx := context.Background()
	_, _ = c.Command(ctx, CmdInit, set1)
	_, _ = c.Command(ctx, CmdStart, set1)
	c.Fault(set1, errors.New("写入因子结果失败：磁盘已满"))
	require.Equal(t, StageFault, c.Stage(set1))
	c.Refresh(ctx)
	require.Equal(t, StageFault, c.Stage(set1), "故障不会被检查自动清除")
	obj, err := c.Command(ctx, CmdResume, set1)
	require.NoError(t, err)
	require.Equal(t, StageWarming, obj.Stage)
}

// 重启后：先未就绪，等启动前检查；通过就回到重启前的阶段并提示「已自动恢复」，不通过保持未就绪并给出原因。
func TestRestartRecoversStageAfterChecksPass(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state", "lifecycle.json")
	f := newFake()
	f.refs = []Ref{set1}
	f.setObject(set1, pass("ok"))
	c := newController(t, f, file)
	ctx := context.Background()
	_, _ = c.Command(ctx, CmdInit, Ref{})
	_, _ = c.Command(ctx, CmdInit, set1)
	_, _ = c.Command(ctx, CmdStart, set1)
	_, _ = c.Command(ctx, CmdConfirm, set1)
	require.Equal(t, StageRunning, c.Stage(set1))

	// 重启：状态从文件读回，对象先未就绪。
	c2 := newController(t, f, file)
	require.Equal(t, StageNotReady, c2.Stage(set1))
	require.False(t, c2.Allowed(set1), "检查通过之前不计算")
	f.setObject(set1, fail("本地数据", "不足"))
	c2.Recover(ctx)
	require.Equal(t, StageNotReady, c2.Stage(set1))
	require.Contains(t, c2.Snapshot().Objects[0].Reason, "本地数据")

	f.setObject(set1, pass("ok"))
	c2.Refresh(ctx)
	require.Equal(t, StageRunning, c2.Stage(set1), "检查通过：回到重启前的阶段")
	require.True(t, c2.Snapshot().Objects[0].Recovered)

	// 在检查通过之前再次重启：仍然记得重启前的阶段。
	f.setObject(set1, fail("x", "y"))
	c3 := newController(t, f, file)
	c3.Recover(ctx)
	f.setObject(set1, pass("ok"))
	c4 := newController(t, f, file)
	c4.Recover(ctx)
	require.Equal(t, StageRunning, c4.Stage(set1))
}

func TestPausedStagePersistsAcrossRestart(t *testing.T) {
	file := filepath.Join(t.TempDir(), "lifecycle.json")
	f := newFake()
	f.refs = []Ref{set1}
	f.setObject(set1, pass("ok"))
	c := newController(t, f, file)
	ctx := context.Background()
	_, _ = c.Command(ctx, CmdInit, set1)
	_, _ = c.Command(ctx, CmdStart, set1)
	_, _ = c.Command(ctx, CmdPause, set1)

	c2 := newController(t, f, file)
	c2.Recover(ctx)
	require.Equal(t, StagePaused, c2.Stage(set1), "手动暂停的对象重启后仍然暂停")
	obj, err := c2.Command(ctx, CmdResume, set1)
	require.NoError(t, err)
	require.Equal(t, StageWarming, obj.Stage)
}

func TestObjectRemovedFromCatalogDisappears(t *testing.T) {
	f := newFake()
	f.refs = []Ref{set1}
	f.setObject(set1, pass("ok"))
	c := newController(t, f, "")
	_, _ = c.Command(context.Background(), CmdInit, set1)
	f.refs = nil
	c.Refresh(context.Background())
	require.Empty(t, c.Snapshot().Objects)
	require.Equal(t, StageUninitialized, c.Stage(set1))
}

func TestSnapshotSummarizesModuleAndObjects(t *testing.T) {
	set2 := Ref{Kind: "factor_set", ID: "set2"}
	f := newFake()
	f.refs = []Ref{set1, set2}
	f.module = []Check{pass("布局")}
	f.setObject(set1, pass("ok"))
	f.setObject(set2, pass("ok"))
	c := newController(t, f, "")
	ctx := context.Background()

	snap := c.Snapshot()
	require.Equal(t, StageUninitialized, snap.Stage)
	require.Equal(t, "gray", snap.Color)

	c.Refresh(ctx) // 模块自身第一次检查时自动初始化
	require.Equal(t, StageReady, c.Stage(Ref{}))
	_, _ = c.Command(ctx, CmdInit, set1)
	_, _ = c.Command(ctx, CmdInit, set2)
	c.Refresh(ctx)
	require.Equal(t, StageReady, c.Snapshot().Stage)

	_, _ = c.Command(ctx, CmdStart, set1)
	_, _ = c.Command(ctx, CmdConfirm, set1)
	_, _ = c.Command(ctx, CmdStart, set2)
	snap = c.Snapshot()
	require.Equal(t, StageWarming, snap.Stage, "有对象在预热，汇总是预热")
	require.False(t, snap.Degraded)

	_, _ = c.Command(ctx, CmdConfirm, set2)
	require.Equal(t, StageRunning, c.Snapshot().Stage)
	require.Equal(t, "green", c.Snapshot().Color)

	// 模块自身检查不过：汇总是未就绪，不看对象。
	f.module = []Check{fail("布局", "版本不一致")}
	c.Refresh(ctx)
	snap = c.Snapshot()
	require.Equal(t, StageNotReady, snap.Stage)
	require.Contains(t, snap.Reason, "布局")
}

func TestInitHookFailureIsReported(t *testing.T) {
	f := newFake()
	f.initFail = errors.New("磁盘不可写")
	c := newController(t, f, "")
	_, err := c.Command(context.Background(), CmdInit, Ref{})
	require.ErrorContains(t, err, "磁盘不可写")
	require.Equal(t, StageUninitialized, c.Stage(Ref{}))
}

func TestHTTPHandlers(t *testing.T) {
	f := newFake()
	f.refs = []Ref{set1}
	f.setObject(set1, pass("ok"))
	c := newController(t, f, "")

	post := func(body string) (*httptest.ResponseRecorder, map[string]any) {
		rec := httptest.NewRecorder()
		c.CommandHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, CommandPath, bytes.NewReader([]byte(body))))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec, out
	}
	rec, out := post(`{"command":"init","kind":"factor_set","id":"set1"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "ready", out["stage"])
	require.Equal(t, "blue", out["color"])

	rec, _ = post(`{"command":"confirm","kind":"factor_set","id":"set1"}`)
	require.Equal(t, http.StatusConflict, rec.Code, "阶段不允许")
	rec, _ = post(`{"command":"start","kind":"factor_set"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, "kind 与 id 要成对")
	rec, _ = post(`{"command":"fly","kind":"factor_set","id":"set1"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	rec, _ = post(`{"bogus":1}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	rec = httptest.NewRecorder()
	c.CommandHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, CommandPath, nil))
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)

	rec = httptest.NewRecorder()
	c.StatusHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, StatusPath, nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var snap Snapshot
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &snap))
	require.Equal(t, "factor-engine", snap.Module)
	require.Len(t, snap.Objects, 1)
	require.Equal(t, StageReady, snap.Objects[0].Stage)
}

func TestOnChangeHookReportsTransitions(t *testing.T) {
	f := newFake()
	f.refs = []Ref{set1}
	f.setObject(set1, pass("ok"))
	c := newController(t, f, "")
	ctx := context.Background()
	_, _ = c.Command(ctx, CmdInit, set1)
	_, _ = c.Command(ctx, CmdStart, set1)
	require.Equal(t, []string{"factor_set/set1:uninitialized>ready", "factor_set/set1:ready>warming"}, f.changes)
}
