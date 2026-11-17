package lifecycle

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPauseKindPriority(t *testing.T) {
	c := newController(t, &fakeModule{caught: true}, "")
	ctx := context.Background()

	state, err := c.Execute(ctx, Request{Command: CmdPause, PauseKind: PauseSystem, Reason: "切换行情源", By: "cli:ops"})
	require.NoError(t, err)
	require.Equal(t, StagePaused, state.Stage)
	require.Equal(t, PauseInfo{Kind: PauseSystem, Reason: "切换行情源", By: "cli:ops", Since: t0}, *state.Pause)
	require.Equal(t, "系统暂停：切换行情源", state.Reason)

	// 手动暂停覆盖系统暂停；系统暂停不覆盖手动暂停。
	state, err = c.Execute(ctx, Request{Command: CmdPause, Reason: "我要停"})
	require.NoError(t, err)
	require.Equal(t, PauseManual, state.Pause.Kind)
	state, err = c.Execute(ctx, Request{Command: CmdPause, PauseKind: PauseSystem})
	require.NoError(t, err)
	require.Equal(t, PauseManual, state.Pause.Kind, "系统暂停不覆盖手动暂停")

	// 系统恢复只恢复系统暂停：手动暂停原样不动。
	state, err = c.Execute(ctx, Request{Command: CmdResume, PauseKind: PauseSystem})
	require.NoError(t, err)
	require.Equal(t, StagePaused, state.Stage)
	state, err = c.Execute(ctx, Request{Command: CmdResume})
	require.NoError(t, err)
	require.Equal(t, StageRunning, state.Stage, "手动恢复，追平后回到运行")
	require.Nil(t, state.Pause)
	require.True(t, state.PausedSince.IsZero())

	_, err = c.Execute(ctx, Request{Command: CmdPause, PauseKind: "bogus"})
	var denied *ErrNotAllowed
	require.ErrorAs(t, err, &denied)
	_, err = c.Execute(ctx, Request{Command: CmdResume})
	require.ErrorAs(t, err, &denied, "运行中不能恢复")
	_, err = c.Execute(ctx, Request{Command: "init"})
	require.ErrorContains(t, err, "未知命令")
}

// 恢复是两段式：追赶期间仍算停下，追平后自动放行回到运行，并记下放行时间。
func TestResumeCatchesUpBeforeRelease(t *testing.T) {
	f := &fakeModule{}
	c := newController(t, f, "")
	ctx := context.Background()
	_, err := c.Execute(ctx, Request{Command: CmdPause})
	require.NoError(t, err)
	require.True(t, c.Halted())

	state, err := c.Execute(ctx, Request{Command: CmdResume})
	require.NoError(t, err)
	require.Equal(t, StageResuming, state.Stage, "没追平：停在恢复追赶")
	require.True(t, c.Halted(), "追赶期间不做实时计算")
	require.False(t, c.Running())
	require.NotNil(t, state.Catchup)
	require.False(t, state.Catchup.CaughtUp)
	require.Equal(t, int64(10), state.Catchup.Total)
	require.Equal(t, t0, state.Catchup.Since)

	c.Refresh(ctx)
	require.Equal(t, StageResuming, c.Stage(), "没追平不会放行")

	f.setCaught(true)
	c.Refresh(ctx)
	require.Equal(t, StageRunning, c.Stage(), "追平后自动放行")
	require.True(t, c.Running())
	require.Equal(t, t0, c.ReleasedAt())
	require.Nil(t, c.State().Catchup)
}

// 系统级恢复：追平后停在待放行，等 release；没追平的 release 被拒绝。
func TestManualReleaseWaitsForBarrier(t *testing.T) {
	f := &fakeModule{}
	c := newController(t, f, "")
	ctx := context.Background()
	_, err := c.Execute(ctx, Request{Command: CmdPause, PauseKind: PauseSystem})
	require.NoError(t, err)
	_, err = c.Execute(ctx, Request{Command: CmdResume, PauseKind: PauseSystem, ManualRelease: true})
	require.NoError(t, err)

	var denied *ErrNotAllowed
	_, err = c.Execute(ctx, Request{Command: CmdRelease})
	require.ErrorAs(t, err, &denied, "没追平不能放行")
	require.False(t, c.Settled(ctx).CaughtUp())

	f.setCaught(true)
	c.Refresh(ctx)
	require.Equal(t, StageResuming, c.Stage(), "追平但设为手动放行：停在待放行")
	rep := c.Settled(ctx)
	require.Equal(t, StageResuming, rep.Stage)
	require.True(t, rep.CaughtUp())

	state, err := c.Execute(ctx, Request{Command: CmdRelease})
	require.NoError(t, err)
	require.Equal(t, StageRunning, state.Stage)
	require.True(t, c.Running())

	_, err = c.Execute(ctx, Request{Command: CmdRelease})
	require.ErrorAs(t, err, &denied, "已经放行")
	rep = c.Settled(ctx)
	require.Equal(t, StageRunning, rep.Stage)
	require.True(t, rep.CaughtUp())
}

// 放行前再核一次：待放行期间又出现缺口（追赶进度回退），release 被拒绝。
func TestReleaseRechecksCatchup(t *testing.T) {
	f := &fakeModule{}
	c := newController(t, f, "")
	ctx := context.Background()
	_, _ = c.Execute(ctx, Request{Command: CmdPause})
	_, err := c.Execute(ctx, Request{Command: CmdResume, ManualRelease: true})
	require.NoError(t, err)
	f.setCaught(true)
	c.Refresh(ctx)
	f.setCaught(false) // 追赶目标前移，又出现缺口
	var denied *ErrNotAllowed
	_, err = c.Execute(ctx, Request{Command: CmdRelease})
	require.ErrorAs(t, err, &denied)
	require.Contains(t, err.Error(), "0/10")
	require.Equal(t, StageResuming, c.Stage())
}

// 恢复追赶中再暂停：回到暂停，暂停起点保持最早的那次，追赶进度清掉。
func TestPauseDuringCatchupKeepsEarliestPausedSince(t *testing.T) {
	now := t0
	c, err := New(Options{Module: "m", Now: func() time.Time { return now }, Hooks: (&fakeModule{}).hooks()})
	require.NoError(t, err)
	ctx := context.Background()
	state, err := c.Execute(ctx, Request{Command: CmdPause})
	require.NoError(t, err)
	require.Equal(t, t0, state.PausedSince)
	now = now.Add(time.Hour)
	_, err = c.Execute(ctx, Request{Command: CmdResume})
	require.NoError(t, err)
	now = now.Add(time.Hour)
	state, err = c.Execute(ctx, Request{Command: CmdPause, Reason: "追赶太慢"})
	require.NoError(t, err)
	require.Equal(t, StagePaused, state.Stage)
	require.Equal(t, t0, state.PausedSince, "暂停起点保持最早的那次")
	require.Equal(t, now, state.Pause.Since)
	require.Nil(t, state.Catchup)
}

// 恢复追赶是进程内的任务：重启后回到暂停，暂停起点与暂停详情不变。
func TestRestartDuringCatchupReturnsToPaused(t *testing.T) {
	file := filepath.Join(t.TempDir(), "lifecycle.json")
	f := &fakeModule{}
	c := newController(t, f, file)
	ctx := context.Background()
	_, _ = c.Execute(ctx, Request{Command: CmdPause, PauseKind: PauseSystem, Reason: "切换行情源"})
	_, err := c.Execute(ctx, Request{Command: CmdResume, ManualRelease: true})
	require.NoError(t, err)
	require.Equal(t, StageResuming, c.Stage())

	c2, err := New(Options{Module: "factor-engine", StateFile: file, Now: func() time.Time { return t0.Add(time.Hour) }, Hooks: f.hooks()})
	require.NoError(t, err)
	c2.Refresh(ctx)
	state := c2.State()
	require.Equal(t, StagePaused, state.Stage, "重启后恢复追赶回到暂停")
	require.Equal(t, t0, state.PausedSince, "暂停起点不丢")
	require.Equal(t, PauseSystem, state.Pause.Kind)
	require.Equal(t, "系统暂停：切换行情源", state.Reason)
	require.Nil(t, state.Catchup)
	require.True(t, c2.Halted())
}

// Halted 只在有意停下时为真：暂停与恢复追赶。
func TestHaltedOnlyForDeliberateStops(t *testing.T) {
	f := &fakeModule{}
	c := newController(t, f, "")
	ctx := context.Background()
	require.False(t, c.Halted())
	_, err := c.Execute(ctx, Request{Command: CmdPause, Reason: "维护"})
	require.NoError(t, err)
	require.True(t, c.Halted())
	_, err = c.Execute(ctx, Request{Command: CmdResume})
	require.NoError(t, err)
	require.True(t, c.Halted(), "恢复追赶中、放行前仍算停下")
	f.setCaught(true)
	c.Refresh(ctx)
	require.False(t, c.Halted())
}

// 暂停后调用 Quiesce 等在途操作结束；没结束时命令返回错误但模块已经暂停。
func TestPauseCallsQuiesceAfterGateCloses(t *testing.T) {
	var stageSeen Stage
	var c *Controller
	hooks := Hooks{Quiesce: func(context.Context) error {
		stageSeen = c.Stage()
		return errors.New("还有 2 个订单请求在途")
	}}
	c, err := New(Options{Module: "trade", Hooks: hooks})
	require.NoError(t, err)

	state, err := c.Execute(context.Background(), Request{Command: CmdPause})
	require.Error(t, err)
	require.Contains(t, err.Error(), "已暂停，但在途操作没有结束")
	require.Equal(t, StagePaused, state.Stage)
	require.Equal(t, StagePaused, stageSeen, "调用 Quiesce 时门禁已经关闭")
	require.True(t, c.Halted())
}

// 追赶钩子拿到的追赶起点是最早一次暂停的时间。
func TestCatchupHookGetsThePausedSince(t *testing.T) {
	var gotSince []time.Time
	now := t0
	c, err := New(Options{Module: "m", Now: func() time.Time { return now }, Hooks: Hooks{
		Catchup: func(_ context.Context, since time.Time) (Catchup, error) {
			gotSince = append(gotSince, since)
			return Catchup{}, nil
		},
	}})
	require.NoError(t, err)
	ctx := context.Background()
	_, err = c.Execute(ctx, Request{Command: CmdPause})
	require.NoError(t, err)
	now = now.Add(time.Hour)
	_, err = c.Execute(ctx, Request{Command: CmdResume})
	require.NoError(t, err)
	c.Refresh(ctx)
	require.NotEmpty(t, gotSince)
	for _, since := range gotSince {
		require.Equal(t, t0, since)
	}
}

// 追赶钩子报错：不算追平，进度里写明读取失败。
func TestCatchupHookErrorIsNotCaughtUp(t *testing.T) {
	c, err := New(Options{Module: "m", Hooks: Hooks{
		Catchup: func(context.Context, time.Time) (Catchup, error) {
			return Catchup{CaughtUp: true}, errors.New("读库失败")
		},
	}})
	require.NoError(t, err)
	ctx := context.Background()
	_, _ = c.Execute(ctx, Request{Command: CmdPause})
	state, err := c.Execute(ctx, Request{Command: CmdResume})
	require.NoError(t, err)
	require.Equal(t, StageResuming, state.Stage)
	require.False(t, state.Catchup.CaughtUp)
	require.Contains(t, state.Catchup.Message, "读库失败")
}

// 定时恢复：暂停时带到期时间，到点后下一轮 Refresh 自动进入恢复追赶，追平后放行；系统暂停与过去的时间被拒绝。
func TestPauseExpiresAutoResumes(t *testing.T) {
	f := &fakeModule{}
	now := t0
	c, err := New(Options{Module: "factor-engine", Now: func() time.Time { return now }, Hooks: f.hooks()})
	require.NoError(t, err)
	ctx := context.Background()

	_, err = c.Execute(ctx, Request{Command: CmdPause, PauseKind: PauseSystem, ExpiresAt: now.Add(time.Hour)})
	require.ErrorContains(t, err, "系统暂停不支持定时恢复")
	_, err = c.Execute(ctx, Request{Command: CmdPause, ExpiresAt: now.Add(-time.Minute)})
	require.ErrorContains(t, err, "晚于现在")

	state, err := c.Execute(ctx, Request{Command: CmdPause, Reason: "夜里停一会", ExpiresAt: now.Add(2 * time.Hour)})
	require.NoError(t, err)
	require.Equal(t, now.Add(2*time.Hour), state.Pause.ExpiresAt)

	now = now.Add(time.Hour)
	c.Refresh(ctx)
	require.Equal(t, StagePaused, c.Stage(), "没到点不恢复")

	now = now.Add(time.Hour)
	c.Refresh(ctx)
	require.Equal(t, StageResuming, c.Stage(), "到点进入恢复追赶，不是直接放行")
	f.setCaught(true)
	c.Refresh(ctx)
	require.True(t, c.Running(), "追平后自动放行")
}

// 暂停、恢复命令成功后各记一条审计事件，带模块、暂停类型、操作者与原因；被拒绝的命令不记。
func TestPauseAndResumeAuditEvents(t *testing.T) {
	var mu sync.Mutex
	var events []string
	c, err := New(Options{Module: "factor-engine", Hooks: (&fakeModule{}).hooks(), Audit: func(event string) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
	}})
	require.NoError(t, err)
	ctx := context.Background()

	_, err = c.Execute(ctx, Request{Command: CmdPause, PauseKind: PauseSystem, Reason: "切换行情源", By: "cli:ops"})
	require.NoError(t, err)
	_, err = c.Execute(ctx, Request{Command: CmdPause, PauseKind: "bogus", By: "cli:ops"})
	require.Error(t, err)
	_, err = c.Execute(ctx, Request{Command: CmdResume, PauseKind: PauseSystem, By: "cli:ops"})
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{
		`lifecycle_pause module=factor-engine kind=system by=cli:ops reason="切换行情源"`,
		`lifecycle_resume module=factor-engine kind=system by=cli:ops reason=""`,
	}, events)
}
