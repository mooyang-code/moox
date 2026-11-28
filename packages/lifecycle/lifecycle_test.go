package lifecycle

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeModule 是被测控制器的钩子：追赶进度可以被测试随时改变，状态变化记下来。
type fakeModule struct {
	mu      sync.Mutex
	caught  bool
	calls   int
	changes []string
}

func (f *fakeModule) setCaught(caught bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.caught = caught
}

func (f *fakeModule) hooks() Hooks {
	return Hooks{
		Catchup: func(context.Context, time.Time) (Catchup, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.calls++
			cu := Catchup{Total: 10, CaughtUp: f.caught}
			if f.caught {
				cu.Done = 10
			}
			return cu, nil
		},
		OnChange: func(from, to Stage, _ string) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.changes = append(f.changes, string(from)+">"+string(to))
		},
	}
}

var t0 = time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)

func newController(t *testing.T, f *fakeModule, file string) *Controller {
	t.Helper()
	c, err := New(Options{Module: "factor-engine", StateFile: file, Now: func() time.Time { return t0 }, Hooks: f.hooks()})
	require.NoError(t, err)
	return c
}

// 新模块就是运行：没有初始化、就绪这些阶段。
func TestNewModuleIsRunning(t *testing.T) {
	c := newController(t, &fakeModule{}, "")
	require.Equal(t, StageRunning, c.Stage())
	require.True(t, c.Running())
	require.False(t, c.Halted())
	require.True(t, c.ReleasedAt().IsZero())
	require.Equal(t, "factor-engine", c.State().Module)
}

// 暂停跨重启保留（暂停详情、起点都在）；运行保留最近一次放行的时间。
func TestPausedStatePersistsAcrossRestart(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state", "lifecycle.json")
	f := &fakeModule{caught: true}
	c := newController(t, f, file)
	ctx := context.Background()
	_, err := c.Execute(ctx, Request{Command: CmdPause, Reason: "维护", By: "ops"})
	require.NoError(t, err)

	c2 := newController(t, f, file)
	state := c2.State()
	require.Equal(t, StagePaused, state.Stage)
	require.Equal(t, PauseManual, state.Pause.Kind)
	require.Equal(t, "维护", state.Pause.Reason)
	require.Equal(t, t0, state.PausedSince)
	require.True(t, c2.Halted())

	_, err = c2.Execute(ctx, Request{Command: CmdResume})
	require.NoError(t, err)
	require.Equal(t, StageRunning, c2.Stage(), "追平即放行")
	c3 := newController(t, f, file)
	require.Equal(t, StageRunning, c3.Stage())
	require.Equal(t, t0, c3.ReleasedAt(), "放行时间跨重启保留")
}

// 状态变化按顺序通知：暂停、恢复追赶、放行。
func TestOnChangeHookReportsTransitions(t *testing.T) {
	f := &fakeModule{caught: true}
	c := newController(t, f, "")
	ctx := context.Background()
	_, _ = c.Execute(ctx, Request{Command: CmdPause})
	_, _ = c.Execute(ctx, Request{Command: CmdResume})
	require.Equal(t, []string{"running>paused", "paused>resuming", "resuming>running"}, f.changes)
}

// 重启后载入的是暂停：Run 一开始以 from 为空通知一次，模块据此把暂停重新落到自己的开关上；载入运行时不通知。
func TestRunNotifiesTheRestoredStateOnce(t *testing.T) {
	file := filepath.Join(t.TempDir(), "lifecycle.json")
	f := &fakeModule{}
	c := newController(t, f, file)
	_, err := c.Execute(context.Background(), Request{Command: CmdPause, PauseKind: PauseSystem})
	require.NoError(t, err)

	restarted := &fakeModule{}
	c2 := newController(t, restarted, file)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); c2.Run(ctx, time.Hour) }()
	require.Eventually(t, func() bool {
		restarted.mu.Lock()
		defer restarted.mu.Unlock()
		return len(restarted.changes) == 1
	}, 3*time.Second, 10*time.Millisecond)
	cancel()
	<-done
	require.Equal(t, []string{">paused"}, restarted.changes)

	running := &fakeModule{}
	c3 := newController(t, running, "")
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	c3.Run(ctx, time.Hour)
	require.Empty(t, running.changes, "载入运行时不通知")
}

// 快照是副本：调用方拿到之后控制器再改暂停详情，快照不变。
func TestStateIsACopy(t *testing.T) {
	c := newController(t, &fakeModule{}, "")
	ctx := context.Background()
	_, err := c.Execute(ctx, Request{Command: CmdPause, PauseKind: PauseSystem, Reason: "a"})
	require.NoError(t, err)
	state := c.State()
	_, err = c.Execute(ctx, Request{Command: CmdPause, Reason: "b"})
	require.NoError(t, err)
	require.Equal(t, "a", state.Pause.Reason)
	require.Equal(t, PauseSystem, state.Pause.Kind)
}

func TestStateFileForDB(t *testing.T) {
	require.Equal(t, filepath.Join("/data/moox/data/trade", StateFileName), StateFileForDB("/data/moox/data/trade/moox_trade.db"))
	require.Equal(t, "", StateFileForDB(""))
	require.Equal(t, "", StateFileForDB(":memory:"))
	require.Equal(t, "", StateFileForDB("file:memdb?mode=memory&cache=shared"))
}
