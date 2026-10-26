package engine

import (
	"context"
	"errors"
	"path/filepath"
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

// 没有水位记录（从未跑过实时周期）的任务，本地同步完成即追平；同步失败会报告并在下一次评估重试。
func TestCatchupSetWithoutWatermarkAndSyncFailure(t *testing.T) {
	r, _ := newCatchupRuntime(t)
	ctx := context.Background()
	since := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	now := since.Add(10 * time.Minute)
	r.catchupSet(ctx, "ftask_a", since, now)
	waitSynced(t, r, "ftask_a", since)
	require.True(t, r.catchupSet(ctx, "ftask_a", since, now).CaughtUp)

	r2, _ := newCatchupRuntime(t)
	r2.preload.data = failingPrepare{err: errors.New("Storage 不可达")}
	r2.catchupSet(ctx, "ftask_a", since, now)
	waitSynced(t, r2, "ftask_a", since)
	got := r2.catchupSet(ctx, "ftask_a", since, now)
	require.False(t, got.CaughtUp)
	require.Contains(t, got.Message, "Storage 不可达")
	require.Nil(t, r2.catchups.get("ftask_a", since), "失败后丢弃，下一次评估重新同步")
}

// 生命周期整体：暂停 → 恢复进入追赶（追赶门禁放行、实时门禁关闭）→ 追平后放行回到运行。
func TestEngineResumeCatchesUpThroughLifecycle(t *testing.T) {
	r, store := newCatchupRuntime(t)
	ctx := context.Background()
	ref := setRef("ftask_a")
	r.life.Refresh(ctx)
	for _, cmd := range []lifecycle.Command{lifecycle.CmdInit, lifecycle.CmdStart, lifecycle.CmdConfirm} {
		_, err := r.life.Command(ctx, cmd, ref)
		require.NoError(t, err)
	}
	_, err := r.life.Command(ctx, lifecycle.CmdPause, ref)
	require.NoError(t, err)
	// 水位还没落后（没有记录）：同步完成即追平。
	_, err = r.life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdResume, Ref: ref})
	require.NoError(t, err)
	require.Equal(t, lifecycle.StageResuming, r.life.Stage(ref), "同步本地数据期间不放行")
	require.False(t, r.gate("ftask_a"))
	require.True(t, r.catchupGate("ftask_a"))
	require.True(t, r.writeGate("ftask_a"))

	require.Eventually(t, func() bool {
		r.life.Refresh(ctx)
		return r.life.Stage(ref) == lifecycle.StageRunning
	}, 15*time.Second, 20*time.Millisecond)
	require.True(t, r.gate("ftask_a"))
	require.False(t, r.catchupGate("ftask_a"))
	_ = store
}
