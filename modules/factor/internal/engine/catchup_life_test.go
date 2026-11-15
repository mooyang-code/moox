package engine

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger/eventconsumer"
	"github.com/mooyang-code/moox/modules/factor/internal/watermark"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/stretchr/testify/require"
)

type noopPeriodReader struct{}

func (noopPeriodReader) ReadWindow(context.Context, storageio.ReadRequest) (map[string]*storageio.Frame, error) {
	return nil, nil
}

type failingPrepare struct{ err error }

func (f failingPrepare) Prepare(context.Context, domain.EngineTask, []string) error { return f.err }
func (f failingPrepare) ReleaseTask(domain.FactorTask)                              {}

// blockingPrepare 让本地数据同步停在 release 关闭之前，测试才能确定地观察“同步中”的状态。
type blockingPrepare struct{ release <-chan struct{} }

func (b blockingPrepare) Prepare(ctx context.Context, _ domain.EngineTask, _ []string) error {
	select {
	case <-b.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (blockingPrepare) ReleaseTask(domain.FactorTask) {}

func newCatchupRuntime(t *testing.T) (*Runtime, *watermark.Store) {
	t.Helper()
	r, _ := newLifecycleRuntime(t)
	store, err := watermark.Open(filepath.Join(t.TempDir(), "watermarks.json"))
	require.NoError(t, err)
	r.continuity, err = eventconsumer.NewContinuity(eventconsumer.ContinuityConfig{Store: store, Reader: noopPeriodReader{}})
	require.NoError(t, err)
	r.advanceKick = make(chan string, 4)
	return r, store
}

func waitSynced(t *testing.T, r *Runtime, taskID string, since time.Time) {
	t.Helper()
	require.Eventually(t, func() bool {
		job := r.catchups.get(taskID, since)
		r.catchups.mu.Lock()
		defer r.catchups.mu.Unlock()
		return job != nil && (job.state == "synced" || job.err != "")
	}, 15*time.Second, 10*time.Millisecond)
}

// 恢复追赶：先同步本地数据；再显式建立追赶目标并唤醒追赶循环；水位追到目标才算追平。
func TestCatchupSetSyncsThenFollowsTheWatermark(t *testing.T) {
	r, store := newCatchupRuntime(t)
	ctx := context.Background()
	since := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	now := time.Date(2026, 10, 21, 10, 10, 20, 0, time.UTC)
	// 暂停前水位在 10:00，目标也在 10:00。
	require.NoError(t, store.Put(watermark.State{TaskID: "ftask_a", SpaceID: "crypto", DatasetID: "dataset_a", Freq: "1m", CommittedThrough: since, Target: since}))

	first := r.catchupSet(ctx, "ftask_a", since, now)
	require.False(t, first.CaughtUp, "第一次评估：本地数据同步刚开始（或刚完成、目标还没建立），不算追平")
	waitSynced(t, r, "ftask_a", since)

	second := r.catchupSet(ctx, "ftask_a", since, now)
	require.False(t, second.CaughtUp)
	// 目标 = 10:10:20 扣 30 秒宽限后对齐到 10:09，再减一个周期 = 10:08。
	require.Equal(t, time.Date(2026, 10, 21, 10, 8, 0, 0, time.UTC), second.Target)
	require.Equal(t, int64(8), second.Total)
	require.Equal(t, int64(0), second.Done)
	state, _ := store.Get("ftask_a")
	require.Equal(t, second.Target, state.Target, "暂停期间事件被 ACK，目标要显式建立")
	select {
	case id := <-r.advanceKick:
		require.Equal(t, "ftask_a", id, "唤醒追赶循环")
	default:
		t.Fatal("没有唤醒连续水位追赶循环")
	}

	// 追赶循环把水位推进了一部分，然后到达目标。
	state.CommittedThrough = since.Add(5 * time.Minute)
	require.NoError(t, store.Put(state))
	mid := r.catchupSet(ctx, "ftask_a", since, now)
	require.False(t, mid.CaughtUp)
	require.Equal(t, int64(5), mid.Done)
	state.CommittedThrough = second.Target
	require.NoError(t, store.Put(state))
	done := r.catchupSet(ctx, "ftask_a", since, now)
	require.True(t, done.CaughtUp)
	require.Equal(t, done.Total, done.Done)
}

// 水位被阻塞（例如读回缺行）：不算追平，并带上卡住的周期与原因。
func TestCatchupSetReportsBlockedWatermark(t *testing.T) {
	r, store := newCatchupRuntime(t)
	ctx := context.Background()
	since := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	now := since.Add(10 * time.Minute)
	require.NoError(t, store.Put(watermark.State{TaskID: "ftask_a", SpaceID: "crypto", DatasetID: "dataset_a", Freq: "1m", CommittedThrough: since, Target: since}))
	r.catchupSet(ctx, "ftask_a", since, now)
	waitSynced(t, r, "ftask_a", since)

	state, _ := store.Get("ftask_a")
	state.BlockedSince, state.BlockedPeriod, state.BlockedReason = now, since.Add(time.Minute), "no_rows"
	require.NoError(t, store.Put(state))
	got := r.catchupSet(ctx, "ftask_a", since, now)
	require.False(t, got.CaughtUp)
	require.Contains(t, got.Message, "no_rows")
}

// 本地数据同步失败会报告原因，并丢弃同步记录让下一次评估重试（不丢弃就会永远卡在失败上）。
// 没有水位记录的任务同步完成即追平，由 TestEngineResumeCatchesUpThroughLifecycle 覆盖。
func TestCatchupSyncFailureIsReportedAndRetried(t *testing.T) {
	ctx := context.Background()
	since := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	now := since.Add(10 * time.Minute)
	r2, _ := newCatchupRuntime(t)
	r2.preload.data = failingPrepare{err: errors.New("Storage 不可达")}
	// 后台同步失败得很快时，发起同步的那次评估自己就会读到失败并丢弃 job，所以不能等 job 出现失败状态，
	// 而是反复评估直到读到失败原因（每次失败都丢弃，下一次评估重新同步）。
	var got lifecycle.Catchup
	require.Eventually(t, func() bool {
		got = r2.catchupSet(ctx, "ftask_a", since, now)
		return strings.Contains(got.Message, "Storage 不可达")
	}, 15*time.Second, 10*time.Millisecond)
	require.False(t, got.CaughtUp)
	require.Nil(t, r2.catchups.get("ftask_a", since), "失败后丢弃，下一次评估重新同步")
}

// 生命周期整体：暂停 → 恢复进入追赶（追赶门禁放行、实时门禁关闭；没追平的任务挡住模块放行）→ 追平后放行回到运行。
func TestEngineResumeCatchesUpThroughLifecycle(t *testing.T) {
	r, _ := newCatchupRuntime(t)
	ctx := context.Background()
	require.NoError(t, r.preload.PrepareSet(ctx, "ftask_a"))
	require.True(t, r.gate("ftask_a"))
	_, err := r.life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdPause})
	require.NoError(t, err)
	// 水位还没落后（没有记录）：同步完成即追平。同步先停住：否则后台同步可能抢在断言之前完成，直接回到运行。
	release := make(chan struct{})
	r.preload.data = blockingPrepare{release: release}
	state, err := r.life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdResume})
	require.NoError(t, err)
	require.Equal(t, lifecycle.StageResuming, state.Stage, "同步本地数据期间不放行")
	require.Equal(t, "因子任务 ftask_a", state.Catchup.BlockedBy, "没追平的任务挡住模块")
	require.False(t, r.gate("ftask_a"))
	require.True(t, r.catchupGate("ftask_a"))
	require.True(t, r.writeGate("ftask_a"))

	close(release)
	require.Eventually(t, func() bool {
		r.life.Refresh(ctx)
		return r.life.Stage() == lifecycle.StageRunning
	}, 15*time.Second, 20*time.Millisecond)
	require.True(t, r.gate("ftask_a"))
	require.False(t, r.catchupGate("ftask_a"))
}

// 除本地窗口外还有不通过项的任务（这里是没有启用成员）不计算，也不参与恢复追赶、不挡住模块放行；
// 只是窗口没载入的任务照常参与：追赶先载入窗口并重查，通过之前报未追平，载入后才追平放行。
func TestTasksFailingChecksDoNotBlockTheModuleRelease(t *testing.T) {
	r, _ := newLifecycleRuntime(t, engineTask("ftask_a", "dataset_a", true, "bias"), engineTask("ftask_b", "dataset_b", true))
	ctx := context.Background()
	r.refreshTaskChecks(ctx) // 两个任务的窗口都没载入；ftask_b 还没有启用的成员
	_, err := r.life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdPause, PauseKind: lifecycle.PauseSystem})
	require.NoError(t, err)
	release := make(chan struct{})
	r.preload.data = blockingPrepare{release: release}
	state, err := r.life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdResume, PauseKind: lifecycle.PauseSystem, ManualRelease: true})
	require.NoError(t, err)
	require.False(t, state.Catchup.CaughtUp, "只是窗口没载入的任务参与追赶：载入之前不算追平")
	require.Equal(t, "因子任务 ftask_a", state.Catchup.BlockedBy)
	require.Nil(t, r.catchups.get("ftask_b", state.Catchup.Since), "除窗口外还有不通过项的任务不参与追赶")

	close(release)
	require.Eventually(t, func() bool { return r.life.Settled(ctx).CaughtUp() }, 15*time.Second, 10*time.Millisecond)
	require.True(t, r.preload.Ready("ftask_a"), "追赶载入了窗口")
	require.True(t, r.catchupGate("ftask_a"), "载入后立即重查，追赶门禁放行")
	require.False(t, r.catchupGate("ftask_b"), "不参与追赶的任务不做追赶写")
	_, err = r.life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdRelease})
	require.NoError(t, err)
	require.True(t, r.gate("ftask_a"))
	require.False(t, r.gate("ftask_b"))
}

// 系统暂停期间重启引擎（预载记录只在内存里，重启后所有任务的窗口都没载入；暂停、恢复追赶期间预载循环不运行），
// 系统恢复时追赶负责载入窗口：窗口载入、检查通过、追平之后才放行，不会以 0/0 立即报追平（否则策略等不到暂停区间的因子结果）。
func TestRestartDuringSystemPauseLoadsWindowsBeforeRelease(t *testing.T) {
	ctx := context.Background()
	stateFile := filepath.Join(t.TempDir(), "lifecycle.json")
	before, _ := newLifecycleRuntimeAt(t, stateFile)
	require.NoError(t, before.preload.PrepareSet(ctx, "ftask_a"))
	require.True(t, before.gate("ftask_a"))
	_, err := before.life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdPause, PauseKind: lifecycle.PauseSystem})
	require.NoError(t, err)

	r, fake := newLifecycleRuntimeAt(t, stateFile) // 重启
	require.Equal(t, lifecycle.StagePaused, r.life.Stage(), "重启后仍是系统暂停")
	r.refreshTaskChecks(ctx)
	r.preload.once(ctx)
	require.Empty(t, fake.prepared, "暂停期间预载循环不把数据拉回本地")
	require.False(t, r.gate("ftask_a"))

	release := make(chan struct{})
	r.preload.data = blockingPrepare{release: release}
	state, err := r.life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdResume, PauseKind: lifecycle.PauseSystem, ManualRelease: true})
	require.NoError(t, err)
	require.Equal(t, lifecycle.StageResuming, state.Stage)
	require.False(t, state.Catchup.CaughtUp, "窗口还没载入：不是 0/0 立即追平")
	require.False(t, r.catchupGate("ftask_a"))
	_, err = r.life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdRelease})
	require.Error(t, err, "没追平不能放行")

	close(release)
	require.Eventually(t, func() bool { return r.life.Settled(ctx).CaughtUp() }, 15*time.Second, 10*time.Millisecond)
	require.True(t, r.preload.Ready("ftask_a"), "恢复追赶载入了窗口")
	require.True(t, r.catchupGate("ftask_a"), "检查通过，任务开始追赶计算")
	_, err = r.life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdRelease})
	require.NoError(t, err)
	require.True(t, r.gate("ftask_a"), "放行后实时计算")
}

// 各任务的追赶并发进行：两个任务同时在推进时才都能结束；一个失败不影响另一个；全部结束后才返回。
func TestAdvanceAllRunsTasksConcurrently(t *testing.T) {
	var running, peak atomic.Int32
	started := make(chan string, 4)
	release := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		advanceAll(context.Background(), []string{"ftask_a", "ftask_b"}, time.Minute, func(_ context.Context, taskID string) error {
			peak.Store(max(peak.Load(), running.Add(1)))
			defer running.Add(-1)
			started <- taskID
			<-release
			if taskID == "ftask_a" {
				return errors.New("暂时不可用")
			}
			return nil
		})
		close(finished)
	}()
	for range 2 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("两个任务应同时开始推进")
		}
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("全部任务结束后应返回")
	}
	require.EqualValues(t, 2, peak.Load())
}

// 每个任务有自己的时限，不被其他任务拖累。
func TestAdvanceAllGivesEachTaskItsOwnDeadline(t *testing.T) {
	deadlines := make(chan time.Time, 2)
	advanceAll(context.Background(), []string{"ftask_a", "ftask_b"}, time.Hour, func(ctx context.Context, _ string) error {
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		deadlines <- deadline
		return nil
	})
	require.Len(t, deadlines, 2)
}
