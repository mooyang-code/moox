package klinecheck

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/stretchr/testify/require"
)

// 强制重拉的分块遇到函数报失败时退回重试：行已经在库里，失败后没有缺口可让自动补采重新发现。
// 每次对账只重试一次（不在原地连续重试），用完 maxChunkAttempts 次之后才记失败。
func TestForcedChunkIsRetriedUntilTheAttemptsRunOut(t *testing.T) {
	backfiller, invoker, _, base := newBackfillFixture(t)
	backfiller.Refill = true
	invoker.fail = true
	ctx := context.Background()
	jobID, chunks, err := backfiller.Submit(ctx, JobRequest{SpaceID: "crypto", SubjectID: "BTC-USDT", From: base, To: base.Add(10 * time.Minute), Force: true}, "manual")
	require.NoError(t, err)
	require.Equal(t, 1, chunks)

	for attempt := 1; attempt <= maxChunkAttempts; attempt++ {
		require.NoError(t, backfiller.Tick(ctx, "crypto"))
		backfiller.Wait()
		require.Len(t, invoker.events, attempt, "一次对账只执行一次，失败后不自己触发补位")
		_, stored, err := backfiller.Store.GetJob(ctx, jobID)
		require.NoError(t, err)
		require.Equal(t, store.BackfillPending, stored[0].Status, "第 %d 次失败后退回待执行", attempt)
		require.Equal(t, attempt, stored[0].Attempts)
		require.Contains(t, stored[0].LastError, "provider error")
		status, err := backfiller.Status(ctx, jobID)
		require.NoError(t, err)
		require.NotEqual(t, store.BackfillFailed, status.Job.Status)
	}

	// 次数用完：下一次认领不再调用函数，分块和作业记失败，带着最后一次的错误。
	step(t, backfiller, 2)
	require.Len(t, invoker.events, maxChunkAttempts)
	status, err := backfiller.Status(ctx, jobID)
	require.NoError(t, err)
	require.Equal(t, store.BackfillFailed, status.Job.Status)
	require.Len(t, status.Failed, 1)
	require.Contains(t, status.Failed[0].LastError, "gave up after 5 attempts")
	require.Contains(t, status.Failed[0].LastError, "provider error")
}

// 重试中途函数恢复正常：作业成功。
func TestForcedChunkSucceedsOnALaterAttempt(t *testing.T) {
	backfiller, invoker, _, base := newBackfillFixture(t)
	invoker.fail = true
	ctx := context.Background()
	jobID, _, err := backfiller.Submit(ctx, JobRequest{SpaceID: "crypto", SubjectID: "BTC-USDT", From: base, To: base.Add(10 * time.Minute), Force: true}, "manual")
	require.NoError(t, err)
	step(t, backfiller, 2)
	status, err := backfiller.Status(ctx, jobID)
	require.NoError(t, err)
	require.NotEqual(t, store.BackfillFailed, status.Job.Status)

	invoker.mu.Lock()
	invoker.fail = false
	invoker.mu.Unlock()
	step(t, backfiller, 2)
	status, err = backfiller.Status(ctx, jobID)
	require.NoError(t, err)
	require.Equal(t, store.BackfillSucceeded, status.Job.Status)
	require.Len(t, invoker.events, 3)
}

// 不是强制重拉的作业（自动补采、普通手工补采）行为不变：函数报失败一次就记失败，不重试。
func TestGapChunkStillFailsOnTheFirstFunctionFailure(t *testing.T) {
	for _, source := range []string{"manual", "auto"} {
		backfiller, invoker, _, base := newBackfillFixture(t)
		backfiller.Refill = true
		invoker.fail = true
		ctx := context.Background()
		jobID, chunks, err := backfiller.Submit(ctx, JobRequest{SpaceID: "crypto", From: base, To: base.Add(time.Hour)}, source)
		require.NoError(t, err)
		require.Equal(t, 2, chunks)
		step(t, backfiller, 2)
		status, err := backfiller.Status(ctx, jobID)
		require.NoError(t, err)
		require.Equal(t, store.BackfillFailed, status.Job.Status, source)
		require.Len(t, status.Failed, 2, source)
		require.Len(t, invoker.events, 2, "%s：每个分块只调用一次", source)
		for _, chunk := range status.Failed {
			require.Equal(t, 1, chunk.Attempts, source)
		}
	}
}
