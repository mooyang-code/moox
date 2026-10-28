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
	f := newFake()
	fail := true
	hooks := f.hooks()
	hooks.Quiesce = func(context.Context, Ref) error {
		if fail {
			return errors.New("还有在途操作")
		}
		return nil
	}
	c, err := New(Options{Module: "m", Hooks: hooks})
	require.NoError(t, err)
	ctx := context.Background()
	c.Refresh(ctx)

	obj, err := c.Command(ctx, CmdPause, Ref{})
	require.Error(t, err)
	require.True(t, obj.QuiescedAt.IsZero())
	require.False(t, c.Snapshot().Self.Quiesced(), "收敛超时不算收敛")

	fail = false
	obj, err = c.Command(ctx, CmdPause, Ref{})
	require.NoError(t, err)
	require.False(t, obj.QuiescedAt.IsZero())
	require.True(t, obj.Quiesced())
	require.True(t, c.Snapshot().Self.Quiesced())
}

// 没有 Quiesce 钩子的对象暂停即视为收敛。
func TestPauseWithoutQuiesceHookIsConverged(t *testing.T) {
	f := newFake()
	c, err := New(Options{Module: "m", Hooks: f.hooks()})
	require.NoError(t, err)
	ctx := context.Background()
	c.Refresh(ctx)
	obj, err := c.Command(ctx, CmdPause, Ref{})
	require.NoError(t, err)
	require.True(t, obj.Quiesced())
}

// 恢复后清空；上一轮暂停的收敛时间不能证明本轮暂停已收敛。
func TestQuiescedAtClearedOnResumeAndNotReusedByNextPause(t *testing.T) {
	f := newFake()
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	hooks := f.hooks()
	blocked := false
	hooks.Quiesce = func(context.Context, Ref) error {
		if blocked {
			return errors.New("在途")
		}
		return nil
	}
	c, err := New(Options{Module: "m", Now: func() time.Time { return now }, Hooks: hooks})
	require.NoError(t, err)
	ctx := context.Background()
	c.Refresh(ctx)
	_, err = c.Command(ctx, CmdPause, Ref{})
	require.NoError(t, err)
	first := c.Snapshot().Self.QuiescedAt
	require.False(t, first.IsZero())

	_, err = c.Command(ctx, CmdResume, Ref{})
	require.NoError(t, err)
	require.True(t, c.Snapshot().Self.QuiescedAt.IsZero(), "恢复后清空")

	// 下一轮暂停：收敛超时，不得沿用上一轮的值。
	now = now.Add(time.Hour)
	blocked = true
	obj, err := c.Command(ctx, CmdPause, Ref{})
	require.Error(t, err)
	require.False(t, obj.Quiesced())
	require.True(t, obj.QuiescedAt.IsZero())
}

// 高优先级暂停覆盖低优先级暂停后，要重新收敛才算：旧的收敛时间早于新的暂停开始时间。
func TestQuiescedRequiresNotBeforePauseSince(t *testing.T) {
	obj := Object{
		Stage: StagePaused,
		Pause: &PauseInfo{Kind: PauseManual, Since: time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC)},
	}
	obj.QuiescedAt = obj.Pause.Since.Add(-time.Minute)
	require.False(t, obj.Quiesced())
	obj.QuiescedAt = obj.Pause.Since
	require.True(t, obj.Quiesced())
	obj.Stage = StageRunning
	require.False(t, obj.Quiesced())
}

// 重启后收敛时间随状态文件恢复（重启时没有在途操作）。
func TestQuiescedAtSurvivesRestart(t *testing.T) {
	f := newFake()
	state := filepath.Join(t.TempDir(), "life.json")
	c, err := New(Options{Module: "m", StateFile: state, Hooks: f.hooks()})
	require.NoError(t, err)
	ctx := context.Background()
	c.Refresh(ctx)
	_, err = c.Command(ctx, CmdPause, Ref{})
	require.NoError(t, err)
	at := c.Snapshot().Self.QuiescedAt
	require.False(t, at.IsZero())

	again, err := New(Options{Module: "m", StateFile: state, Hooks: f.hooks()})
	require.NoError(t, err)
	again.Refresh(ctx)
	self := again.Snapshot().Self
	require.Equal(t, StagePaused, self.Stage)
	require.True(t, self.QuiescedAt.Equal(at))
	require.True(t, self.Quiesced())
}

// 暂停代次：模块每次从放行变为不放行加一，重复查询不重复计数。
func TestModuleGenerationAdvancesOnEachClose(t *testing.T) {
	f := newFake()
	c, err := New(Options{Module: "m", Hooks: f.hooks()})
	require.NoError(t, err)
	ctx := context.Background()
	c.Refresh(ctx)
	require.True(t, c.ModuleOpen())
	g0 := c.ModuleGeneration()
	require.Equal(t, g0, c.ModuleGeneration())

	_, err = c.Command(ctx, CmdPause, Ref{})
	require.NoError(t, err)
	require.False(t, c.ModuleOpen())
	g1 := c.ModuleGeneration()
	require.Equal(t, g0+1, g1)
	require.Equal(t, g1, c.ModuleGeneration())

	_, err = c.Command(ctx, CmdResume, Ref{})
	require.NoError(t, err)
	c.Refresh(ctx)
	require.True(t, c.ModuleOpen())
	require.Equal(t, g1, c.ModuleGeneration(), "重新放行不改变代次")

	_, err = c.Command(ctx, CmdPause, Ref{})
	require.NoError(t, err)
	require.Equal(t, g1+1, c.ModuleGeneration())
}

// 等待是针对某一轮暂停做的：上一轮的等待结果在期间恢复又暂停之后，不能把下一轮误报为已收敛。
func TestLateQuiesceOfAnEarlierRoundDoesNotConfirmTheNextRound(t *testing.T) {
	f := newFake()
	entered := make(chan struct{})
	release := make(chan struct{})
	round := 0
	hooks := f.hooks()
	hooks.Quiesce = func(ctx context.Context, _ Ref) error {
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
	c.Refresh(ctx)

	first := make(chan error, 1)
	go func() { _, err := c.Command(ctx, CmdPause, Ref{}); first <- err }()
	<-entered
	_, err = c.Command(ctx, CmdResume, Ref{})
	require.NoError(t, err)
	c.Refresh(ctx)
	_, err = c.Command(ctx, CmdPause, Ref{})
	require.Error(t, err, "第二轮收敛超时")
	require.False(t, c.Snapshot().Self.Quiesced())

	close(release)
	require.NoError(t, <-first)
	require.False(t, c.Snapshot().Self.Quiesced(), "第一轮迟到的等待结果不能确认第二轮")
	require.True(t, c.Snapshot().Self.QuiescedAt.IsZero())
}

// 重启后待恢复的暂停（启动前检查还没通过）仍算被有意停下，写路径不能因此放开。
func TestHaltedStaysTrueForARestoredPauseAwaitingChecks(t *testing.T) {
	f := newFake()
	state := filepath.Join(t.TempDir(), "life.json")
	c, err := New(Options{Module: "m", StateFile: state, Hooks: f.hooks()})
	require.NoError(t, err)
	ctx := context.Background()
	c.Refresh(ctx)
	_, err = c.Command(ctx, CmdPause, Ref{})
	require.NoError(t, err)

	// 重启后模块自身的检查暂时不过：控制器是“未就绪、待恢复暂停”。
	f.mu.Lock()
	f.module = []Check{fail("依赖", "暂不可用")}
	f.mu.Unlock()
	again, err := New(Options{Module: "m", StateFile: state, Hooks: f.hooks()})
	require.NoError(t, err)
	again.Refresh(ctx)
	require.Equal(t, StageNotReady, again.Stage(Ref{}))
	require.True(t, again.Halted(Ref{}), "待恢复的暂停仍然是被有意停下")
}

// 上一轮恢复追赶迟到的“已追平”结果，不能覆盖新一轮、更不能放行新一轮。
func TestLateCatchupResultFromAnEarlierRoundDoesNotReleaseTheNextRound(t *testing.T) {
	f := newFake()
	var mu sync.Mutex
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	tick := func() { mu.Lock(); now = now.Add(time.Minute); mu.Unlock() }
	entered, release := make(chan struct{}), make(chan struct{})
	var calls int32
	hooks := f.hooks()
	hooks.Catchup = func(context.Context, Ref, time.Time) (Catchup, error) {
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
	c.Refresh(ctx)
	_, err = c.Command(ctx, CmdPause, Ref{})
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = c.Command(ctx, CmdResume, Ref{}) // 第一轮：卡在 Catchup 钩子里
	}()
	<-entered
	tick()
	_, err = c.Command(ctx, CmdPause, Ref{})
	require.NoError(t, err)
	tick()
	obj, err := c.Command(ctx, CmdResume, Ref{}) // 第二轮：钩子返回未追平
	require.NoError(t, err)
	require.Equal(t, StageResuming, obj.Stage)

	close(release) // 第一轮的旧结果迟到
	<-done
	require.Equal(t, StageResuming, c.Stage(Ref{}), "迟到的旧结果不能放行新一轮")
}
