package recalcexec

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/setlock"
	"github.com/stretchr/testify/require"
)

// 这些测试用真的 setlock：补算块在 runner 里停住时，看实时周期（任务锁）与其他后台路径（补算锁）能不能拿到锁。

// blockingRunner 每次 Run 先报告已进入，再等 release 关闭（或 ctx 结束）。
type blockingRunner struct {
	entered chan pipeline.Plan
	release chan struct{}
}

func newBlockingRunner() *blockingRunner {
	return &blockingRunner{entered: make(chan pipeline.Plan, 16), release: make(chan struct{})}
}

func (r *blockingRunner) Run(ctx context.Context, plan pipeline.Plan) (pipeline.Outcome, error) {
	r.entered <- plan
	select {
	case <-r.release:
		return pipeline.Outcome{Status: "complete"}, nil
	case <-ctx.Done():
		return pipeline.Outcome{}, ctx.Err()
	}
}

func (r *blockingRunner) waitEntered(t *testing.T) pipeline.Plan {
	t.Helper()
	select {
	case plan := <-r.entered:
		return plan
	case <-time.After(time.Second):
		t.Fatal("补算块没有在 1 秒内开始运行")
		return pipeline.Plan{}
	}
}

type lockFunc func(context.Context, string) (func(), error)

// requireLockable 断言这把锁在 1 秒内能拿到（拿到后立刻释放）。
func requireLockable(t *testing.T, lock lockFunc, msg string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	unlock, err := lock(ctx, taskKey)
	require.NoError(t, err, msg)
	unlock()
}

// requireLocked 断言这把锁在 30ms 内拿不到（被别人持有）。
func requireLocked(t *testing.T, lock lockFunc, msg string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	unlock, err := lock(ctx, taskKey)
	if err == nil {
		unlock()
	}
	require.ErrorIs(t, err, context.DeadlineExceeded, msg)
}

func watermarkAt(at time.Time) func(string) (time.Time, bool) {
	return func(taskID string) (time.Time, bool) { return at, taskID == taskKey }
}

func noWatermark(string) (time.Time, bool) { return time.Time{}, false }

// 补算块只写水位以内的周期：块在运行时实时周期照样拿得到任务锁，其他后台路径要等补算锁；分块与不加锁时完全相同。
func TestRecalcChunkWithinWatermarkDoesNotBlockLive(t *testing.T) {
	locks := setlock.New()
	window := testWindow(6)
	runner := newBlockingRunner()
	executor := NewExecutor(runner, WithChunkPeriods(2), WithLocks(locks), WithWatermark(watermarkAt(window.End.Add(-time.Minute))))
	var records []progressRecord
	done := make(chan error, 1)
	go func() {
		done <- executor.Run(context.Background(), window, fixedSource(executorSelection("BTC")), recordProgress(&records, 0))
	}()

	first := runner.waitEntered(t)
	requireLockable(t, locks.LockContext, "补算块运行时实时周期必须立刻拿到任务锁")
	requireLocked(t, locks.LockRecalcContext, "补算块运行时补算锁被它持有")
	close(runner.release)
	require.NoError(t, <-done)

	plans := []pipeline.Plan{first}
	for len(runner.entered) > 0 {
		plans = append(plans, <-runner.entered)
	}
	plain := &recordingRunner{}
	var plainRecords []progressRecord
	require.NoError(t, NewExecutor(plain, WithChunkPeriods(2)).Run(context.Background(), window, fixedSource(executorSelection("BTC")), recordProgress(&plainRecords, 0)))
	require.Equal(t, plain.snapshot(), plans, "加锁方式不改变分块与计划")
	require.Len(t, records, 3)
	require.Equal(t, window.End, records[2].progress)
	requireLockable(t, locks.LockContext, "结束后任务锁已释放")
	requireLockable(t, locks.LockRecalcContext, "结束后补算锁已释放")
}

// 块的最后一期晚于水位（或任务还没有水位）时持有任务锁；恰好等于水位时不持有。
func TestRecalcChunkTakesTaskLockOnlyPastWatermark(t *testing.T) {
	window := testWindow(2) // 一块，两期：start、start+1m
	cases := []struct {
		name      string
		watermark func(string) (time.Time, bool)
		holdsTask bool
	}{
		{name: "最后一期等于水位", watermark: watermarkAt(window.Start.Add(time.Minute)), holdsTask: false},
		{name: "最后一期晚于水位一期", watermark: watermarkAt(window.Start), holdsTask: true},
		{name: "还没有水位", watermark: noWatermark, holdsTask: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			locks := setlock.New()
			runner := newBlockingRunner()
			executor := NewExecutor(runner, WithChunkPeriods(2), WithLocks(locks), WithWatermark(tc.watermark))
			var records []progressRecord
			done := make(chan error, 1)
			go func() {
				done <- executor.Run(context.Background(), window, fixedSource(executorSelection("BTC")), recordProgress(&records, 0))
			}()
			runner.waitEntered(t)
			if tc.holdsTask {
				requireLocked(t, locks.LockContext, "跨过水位的块必须持有任务锁")
			} else {
				requireLockable(t, locks.LockContext, "水位以内的块不持有任务锁")
			}
			close(runner.release)
			require.NoError(t, <-done)
			requireLockable(t, locks.LockContext, "结束后任务锁已释放")
		})
	}
}

// 实时周期正持有任务锁：水位以内的补算块不等它；跨过水位的块等它释放后才开始。
func TestLiveTaskLockOnlyDelaysChunksPastWatermark(t *testing.T) {
	locks := setlock.New()
	window := testWindow(2)
	liveUnlock, err := locks.LockContext(context.Background(), taskKey)
	require.NoError(t, err)

	historical := NewExecutor(&recordingRunner{}, WithChunkPeriods(2), WithLocks(locks), WithWatermark(watermarkAt(window.End)))
	var records []progressRecord
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, historical.Run(ctx, window, fixedSource(executorSelection("BTC")), recordProgress(&records, 0)), "水位以内的块不等实时周期")

	runner := newBlockingRunner()
	close(runner.release)
	frontier := NewExecutor(runner, WithChunkPeriods(2), WithLocks(locks), WithWatermark(watermarkAt(window.Start)))
	done := make(chan error, 1)
	go func() {
		done <- frontier.Run(context.Background(), window, fixedSource(executorSelection("BTC")), recordProgress(&records, 0))
	}()
	select {
	case <-runner.entered:
		t.Fatal("跨过水位的块不能在实时周期持有任务锁时运行")
	case <-time.After(30 * time.Millisecond):
	}
	liveUnlock()
	runner.waitEntered(t)
	require.NoError(t, <-done)
}

// 补算作业与缺口恢复、失效重算（RunChunkWith）共用补算锁：补算块没结束，水位以内的恢复也不会开始。
func TestRunChunkWithWaitsForRecalcChunk(t *testing.T) {
	locks := setlock.New()
	window := testWindow(2)
	blocking := newBlockingRunner()
	recalc := NewExecutor(blocking, WithChunkPeriods(2), WithLocks(locks), WithWatermark(watermarkAt(window.End)))
	var records []progressRecord
	recalcDone := make(chan error, 1)
	go func() {
		recalcDone <- recalc.Run(context.Background(), window, fixedSource(executorSelection("BTC")), recordProgress(&records, 0))
	}()
	blocking.waitEntered(t)

	recovery := newBlockingRunner()
	close(recovery.release)
	filler := NewExecutor(recovery, WithLocks(locks), WithWatermark(watermarkAt(window.End)))
	fillDone := make(chan error, 1)
	go func() {
		_, err := filler.RunChunkWith(context.Background(), fixedSource(executorSelection("BTC")), window.Start, window.Start.Add(time.Minute), nil)
		fillDone <- err
	}()
	select {
	case <-recovery.entered:
		t.Fatal("补算块持有补算锁时恢复不能运行")
	case <-time.After(30 * time.Millisecond):
	}
	requireLockable(t, locks.LockContext, "两者都在水位以内，实时周期不受影响")
	close(blocking.release)
	require.NoError(t, <-recalcDone)
	recovery.waitEntered(t)
	require.NoError(t, <-fillDone)
}

// 等锁时取消：补算锁或任务锁都不泄漏；拿到补算锁后在等任务锁时取消，已拿到的补算锁要释放。
func TestCancelWhileWaitingForLocksDoesNotLeak(t *testing.T) {
	window := testWindow(2)
	for _, held := range []struct {
		name string
		lock func(*setlock.Locks) lockFunc
	}{
		{name: "等补算锁", lock: func(l *setlock.Locks) lockFunc { return l.LockRecalcContext }},
		{name: "等任务锁", lock: func(l *setlock.Locks) lockFunc { return l.LockContext }},
	} {
		t.Run(held.name, func(t *testing.T) {
			locks := setlock.New()
			unlock, err := held.lock(locks)(context.Background(), taskKey)
			require.NoError(t, err)
			runner := newBlockingRunner()
			executor := NewExecutor(runner, WithChunkPeriods(2), WithLocks(locks), WithWatermark(noWatermark))
			ctx, cancel := context.WithCancel(context.Background())
			// 锁外的探测读一次选择之后紧接着就去取锁：等到探测、再稍等一下，取消时 Run 已经在等锁。
			probed := make(chan struct{})
			source := func(context.Context) (Selection, error) {
				select {
				case <-probed:
				default:
					close(probed)
				}
				return executorSelection("BTC"), nil
			}
			var records []progressRecord
			done := make(chan error, 1)
			go func() {
				done <- executor.Run(ctx, window, source, recordProgress(&records, 0))
			}()
			<-probed
			time.Sleep(10 * time.Millisecond)
			cancel()
			select {
			case err := <-done:
				require.ErrorIs(t, err, context.Canceled)
			case <-time.After(time.Second):
				t.Fatal("取消后 Run 没有在 1 秒内返回")
			}
			require.Empty(t, runner.entered, "没拿到锁就不运行")
			require.Empty(t, records)
			unlock()
			requireLockable(t, locks.LockRecalcContext, "补算锁没有泄漏")
			requireLockable(t, locks.LockContext, "任务锁没有泄漏")
		})
	}
}

// 门禁挡住（暂停、恢复追赶）时返回 ErrGated，补算锁与任务锁都已释放。
func TestGatedChunkReleasesLocks(t *testing.T) {
	locks := setlock.New()
	window := testWindow(2)
	runner := &recordingRunner{}
	executor := NewExecutor(runner, WithChunkPeriods(2), WithLocks(locks), WithWatermark(noWatermark),
		WithWriteScope(func(context.Context, string) (func(), bool) { return nil, false }))
	var records []progressRecord
	require.ErrorIs(t, executor.Run(context.Background(), window, fixedSource(executorSelection("BTC")), recordProgress(&records, 0)), ErrGated)
	_, err := executor.RunChunkWith(context.Background(), fixedSource(executorSelection("BTC")), window.Start, window.End, nil)
	require.True(t, errors.Is(err, ErrGated))
	require.Empty(t, runner.snapshot())
	requireLockable(t, locks.LockRecalcContext, "补算锁已释放")
	requireLockable(t, locks.LockContext, "任务锁已释放")
}
