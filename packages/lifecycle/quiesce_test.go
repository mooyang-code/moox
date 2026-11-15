package lifecycle

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 钩子成功返回才写收敛时间；超时（钩子返回错误）不写。
func TestQuiescedAtWrittenOnlyAfterQuiesceSucceeds(t *testing.T) {
	fail := true
	hooks := Hooks{}
	hooks.Quiesce = func(context.Context) error {
		if fail {
			return errors.New("还有在途操作")
		}
		return nil
	}
	c, err := New(Options{Module: "m", Hooks: hooks})
	require.NoError(t, err)
	ctx := context.Background()

	state, err := c.Execute(ctx, Request{Command: CmdPause})
	require.Error(t, err)
	require.True(t, state.QuiescedAt.IsZero())
	require.False(t, c.State().Quiesced(), "收敛超时不算收敛")

	fail = false
	state, err = c.Execute(ctx, Request{Command: CmdPause})
	require.NoError(t, err)
	require.False(t, state.QuiescedAt.IsZero())
	require.True(t, state.Quiesced())
	require.True(t, c.State().Quiesced())
}

// 没有 Quiesce 钩子的模块暂停即视为收敛。
func TestPauseWithoutQuiesceHookIsConverged(t *testing.T) {
	c, err := New(Options{Module: "m"})
	require.NoError(t, err)
	state, err := c.Execute(context.Background(), Request{Command: CmdPause})
	require.NoError(t, err)
	require.True(t, state.Quiesced())
}

// 恢复后清空；上一轮暂停的收敛时间不能证明本轮暂停已收敛。
func TestQuiescedAtClearedOnResumeAndNotReusedByNextPause(t *testing.T) {
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	hooks := Hooks{}
	blocked := false
	hooks.Quiesce = func(context.Context) error {
		if blocked {
			return errors.New("在途")
		}
		return nil
	}
	c, err := New(Options{Module: "m", Now: func() time.Time { return now }, Hooks: hooks})
	require.NoError(t, err)
	ctx := context.Background()
	_, err = c.Execute(ctx, Request{Command: CmdPause})
	require.NoError(t, err)
	first := c.State().QuiescedAt
	require.False(t, first.IsZero())

	_, err = c.Execute(ctx, Request{Command: CmdResume})
	require.NoError(t, err)
	require.True(t, c.State().QuiescedAt.IsZero(), "恢复后清空")

	// 下一轮暂停：收敛超时，不得沿用上一轮的值。
	now = now.Add(time.Hour)
	blocked = true
	state, err := c.Execute(ctx, Request{Command: CmdPause})
	require.Error(t, err)
	require.False(t, state.Quiesced())
	require.True(t, state.QuiescedAt.IsZero())
}

// 高优先级暂停覆盖低优先级暂停后，要重新收敛才算：旧的收敛时间早于新的暂停开始时间。
func TestQuiescedRequiresNotBeforePauseSince(t *testing.T) {
	state := State{
		Stage: StagePaused,
		Pause: &PauseInfo{Kind: PauseManual, Since: time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC)},
	}
	state.QuiescedAt = state.Pause.Since.Add(-time.Minute)
	require.False(t, state.Quiesced())
	state.QuiescedAt = state.Pause.Since
	require.True(t, state.Quiesced())
	state.Stage = StageRunning
	require.False(t, state.Quiesced())
}

// 重启后收敛时间随状态文件恢复（重启时没有在途操作）。
func TestQuiescedAtSurvivesRestart(t *testing.T) {
	file := filepath.Join(t.TempDir(), "life.json")
	c, err := New(Options{Module: "m", StateFile: file})
	require.NoError(t, err)
	ctx := context.Background()
	_, err = c.Execute(ctx, Request{Command: CmdPause})
	require.NoError(t, err)
	at := c.State().QuiescedAt
	require.False(t, at.IsZero())

	again, err := New(Options{Module: "m", StateFile: file})
	require.NoError(t, err)
	again.Refresh(ctx)
	self := again.State()
	require.Equal(t, StagePaused, self.Stage)
	require.True(t, self.QuiescedAt.Equal(at))
	require.True(t, self.Quiesced())
}

// 等待是针对某一轮暂停做的：上一轮的等待结果在期间恢复又暂停之后，不能把下一轮误报为已收敛。
func TestLateQuiesceOfAnEarlierRoundDoesNotConfirmTheNextRound(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	round := 0
	hooks := Hooks{}
	hooks.Quiesce = func(ctx context.Context) error {
		round++
		if round == 1 {
			close(entered)
			<-release // 第一轮的等待被卡住
			return nil
		}
		return errors.New("第二轮还有在途操作")
	}
	c, err := New(Options{Module: "m", Hooks: hooks, QuiesceTimeout: time.Minute})
	require.NoError(t, err)
	ctx := context.Background()

	first := make(chan error, 1)
	go func() { _, err := c.Execute(ctx, Request{Command: CmdPause}); first <- err }()
	<-entered
	_, err = c.Execute(ctx, Request{Command: CmdResume})
	require.NoError(t, err)
	c.Refresh(ctx)
	_, err = c.Execute(ctx, Request{Command: CmdPause})
	require.Error(t, err, "第二轮收敛超时")
	require.False(t, c.State().Quiesced())

	close(release)
	require.NoError(t, <-first)
	require.False(t, c.State().Quiesced(), "第一轮迟到的等待结果不能确认第二轮")
	require.True(t, c.State().QuiescedAt.IsZero())
}

// 重启后暂停仍算被有意停下，写路径不能因此放开。
func TestHaltedStaysTrueForARestoredPause(t *testing.T) {
	file := filepath.Join(t.TempDir(), "life.json")
	c, err := New(Options{Module: "m", StateFile: file})
	require.NoError(t, err)
	_, err = c.Execute(context.Background(), Request{Command: CmdPause})
	require.NoError(t, err)

	again, err := New(Options{Module: "m", StateFile: file})
	require.NoError(t, err)
	require.Equal(t, StagePaused, again.Stage())
	require.True(t, again.Halted(), "重启后仍然停着")
}

// 上一轮恢复追赶迟到的“已追平”结果，不能覆盖新一轮、更不能放行新一轮。
func TestLateCatchupResultFromAnEarlierRoundDoesNotReleaseTheNextRound(t *testing.T) {
	var mu sync.Mutex
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	tick := func() { mu.Lock(); now = now.Add(time.Minute); mu.Unlock() }
	entered, release := make(chan struct{}), make(chan struct{})
	var calls int32
	hooks := Hooks{}
	hooks.Catchup = func(context.Context, time.Time) (Catchup, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			close(entered)
			<-release
			return Catchup{CaughtUp: true}, nil // 上一轮的旧结果
		}
		return Catchup{CaughtUp: false, Message: "新一轮还没追平"}, nil
	}
	c, err := New(Options{Module: "m", Now: clock, Hooks: hooks})
	require.NoError(t, err)
	ctx := context.Background()
	_, err = c.Execute(ctx, Request{Command: CmdPause})
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = c.Execute(ctx, Request{Command: CmdResume}) // 第一轮：卡在 Catchup 钩子里
	}()
	<-entered
	tick()
	_, err = c.Execute(ctx, Request{Command: CmdPause})
	require.NoError(t, err)
	tick()
	state, err := c.Execute(ctx, Request{Command: CmdResume}) // 第二轮：钩子返回未追平
	require.NoError(t, err)
	require.Equal(t, StageResuming, state.Stage)

	close(release) // 第一轮的旧结果迟到
	<-done
	require.Equal(t, StageResuming, c.Stage(), "迟到的旧结果不能放行新一轮")
}
