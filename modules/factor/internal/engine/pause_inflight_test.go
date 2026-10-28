package engine

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/recalcexec"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/stretchr/testify/require"
)

// startRunningModule 让测试运行时的任务进入运行：模块自身放行、任务运行。
func startRunningModule(t *testing.T, r *Runtime) {
	t.Helper()
	r.writes = newInflight()
	ctx := context.Background()
	r.life.Refresh(ctx)
	for _, cmd := range []lifecycle.Command{lifecycle.CmdInit, lifecycle.CmdStart, lifecycle.CmdConfirm} {
		_, err := r.life.Command(ctx, cmd, setRef("ftask_a"))
		require.NoError(t, err)
	}
}

// 暂停等在途的写结果操作结束才算收敛：登记之后的写操作没有结束时，Quiesce 一直等；结束后才写入收敛证据。
func TestPauseWaitsForRegisteredWritesBeforeQuiesced(t *testing.T) {
	r, _ := newLifecycleRuntime(t)
	startRunningModule(t, r)
	ctx := context.Background()

	release, ok := r.beginWrite(ctx, "ftask_a", true, false)
	require.True(t, ok)

	done := make(chan lifecycle.Object, 1)
	go func() {
		obj, _ := r.life.Command(ctx, lifecycle.CmdPause, lifecycle.Ref{})
		done <- obj
	}()
	require.Eventually(t, func() bool { return !r.life.ModuleOpen() }, 5*time.Second, 5*time.Millisecond)
	select {
	case <-done:
		t.Fatal("在途操作还没结束，暂停不应返回收敛")
	case <-time.After(100 * time.Millisecond):
	}
	require.False(t, r.life.Snapshot().Self.Quiesced())

	release()
	select {
	case obj := <-done:
		require.True(t, obj.Quiesced())
	case <-time.After(5 * time.Second):
		t.Fatal("在途操作结束后暂停应当收敛")
	}
	require.True(t, r.lifecycleStatus().Quiesced)
	require.Equal(t, "paused", r.lifecycleStatus().Stage)
}

// 暂停之后：新的写操作被拒绝且不留在途登记；不领取补算；回放写回被拒绝。
func TestPausedModuleRejectsWritesLeasesAndReplayWrites(t *testing.T) {
	r, _ := newLifecycleRuntime(t)
	startRunningModule(t, r)
	ctx := context.Background()
	_, err := r.life.Command(ctx, lifecycle.CmdPause, lifecycle.Ref{})
	require.NoError(t, err)

	_, ok := r.beginWrite(ctx, "ftask_a", true, true)
	require.False(t, ok)
	_, ok = r.beginReplayWrite(ctx, "ftask_a")
	require.False(t, ok)
	require.Zero(t, r.writes.count(""), "被拒绝的操作不留登记")
	require.False(t, r.leaseAllowed())

	client := &jobClientFake{job: testJob("BTC"), found: true}
	loop := &recalcLoop{client: client, executor: recalcexec.NewExecutor(&planRecorder{}), source: &sourceFake{}, poll: time.Millisecond,
		leaseAllowed: r.leaseAllowed, generation: r.life.ModuleGeneration, moduleOpen: r.life.ModuleOpen, pulling: func() func() { return r.writes.enter("") }}
	found, err := loop.RunOnce(ctx)
	require.NoError(t, err)
	require.False(t, found)
	require.Zero(t, client.pulled, "暂停期间不领取补算")
}

// 领取请求在途时暂停要等它返回；领取回来的作业带着领取时的代次，之后暂停过就不能再写。
func TestLeaseGenerationInvalidatesOldJobContext(t *testing.T) {
	r, _ := newLifecycleRuntime(t)
	startRunningModule(t, r)
	ctx := context.Background()

	old := withLeaseGeneration(ctx, r.life.ModuleGeneration())
	release, ok := r.beginWrite(old, "ftask_a", true, true)
	require.True(t, ok)
	release()

	// 暂停、恢复进入追赶：追赶门禁放行，但旧的执行上下文代次已过期。
	_, err := r.life.Command(ctx, lifecycle.CmdPause, lifecycle.Ref{})
	require.NoError(t, err)
	_, err = r.life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdResume, Ref: lifecycle.Ref{}, ManualRelease: true})
	require.NoError(t, err)
	require.True(t, r.catchupGate("ftask_a"), "追赶门禁放行（没有目录同步器时不要求前置条件）")

	_, ok = r.beginWrite(old, "ftask_a", true, true)
	require.False(t, ok, "领取之后暂停过，旧上下文不能再写")
	fresh := withLeaseGeneration(ctx, r.life.ModuleGeneration())
	release, ok = r.beginWrite(fresh, "ftask_a", true, true)
	require.True(t, ok)
	release()
}

// 模块暂停时补算作业立刻放弃，不在门禁重开后继续（作业只能重新领取）；任务级的门禁等待仍然保留。
func TestRecalcJobIsAbandonedWhenTheModulePauses(t *testing.T) {
	r, _ := newLifecycleRuntime(t)
	startRunningModule(t, r)
	ctx := context.Background()
	client := &jobClientFake{job: testJob("BTC"), found: true}
	runner := &planRecorder{out: pipeline.Outcome{Status: "complete"}}
	executor := recalcexec.NewExecutor(runner, recalcexec.WithChunkPeriods(2), recalcexec.WithChunkRetry(1, time.Millisecond),
		recalcexec.WithWriteScope(func(ctx context.Context, taskID string) (func(), bool) {
			return r.beginWrite(ctx, taskID, true, true)
		}))
	loop := &recalcLoop{client: client, executor: executor, source: &sourceFake{}, poll: time.Millisecond,
		gateWait: 5 * time.Second, gateRetry: time.Millisecond,
		leaseAllowed: r.leaseAllowed, generation: r.life.ModuleGeneration, moduleOpen: r.life.ModuleOpen, pulling: func() func() { return r.writes.enter("") }}

	// 第一块跑完后暂停模块，再下一块被门禁挡住；作业不能等到“门禁重开”。
	var chunks int
	loop.executor = recalcexec.NewExecutor(&hookRunner{inner: runner, after: func() {
		chunks++
		if chunks == 1 {
			// 暂停命令要等这一块的在途登记结束才返回，所以放在后台，只等它先把门禁关上。
			go func() { _, _ = r.life.Command(ctx, lifecycle.CmdPause, lifecycle.Ref{}) }()
			require.Eventually(t, func() bool { return !r.life.ModuleOpen() }, 5*time.Second, time.Millisecond)
		}
	}}, recalcexec.WithChunkPeriods(2), recalcexec.WithChunkRetry(1, time.Millisecond),
		recalcexec.WithWriteScope(func(ctx context.Context, taskID string) (func(), bool) {
			return r.beginWrite(ctx, taskID, true, true)
		}))

	started := time.Now()
	found, err := loop.RunOnce(ctx)
	require.NoError(t, err)
	require.False(t, found, "被门禁挡住按没有任务处理")
	require.Less(t, time.Since(started), 2*time.Second, "不等待门禁重开")
	require.Len(t, runner.plans, 1, "暂停之后没有再写任何分块")
	require.NotContains(t, reportedStatuses(client), jobStatusFailed)
}

type hookRunner struct {
	inner *planRecorder
	after func()
}

func (h *hookRunner) Run(ctx context.Context, plan pipeline.Plan) (pipeline.Outcome, error) {
	out, err := h.inner.Run(ctx, plan)
	h.after()
	return out, err
}

func reportedStatuses(c *jobClientFake) []string {
	var out []string
	for _, r := range c.reports {
		out = append(out, r.status)
	}
	return out
}

// 追赶前置条件：恢复开始之后目录至少成功同步过一次才允许追赶写；之前（含暂停前的同步）都不算。
func TestCatchupWaitsForACatalogSyncAfterResume(t *testing.T) {
	r, _ := newLifecycleRuntime(t)
	startRunningModule(t, r)
	ctx := context.Background()
	r.syncer = &catalogSyncer{cache: r.catalog, kick: make(chan struct{}, 1), now: time.Now}

	_, err := r.life.Command(ctx, lifecycle.CmdPause, lifecycle.Ref{})
	require.NoError(t, err)
	time.Sleep(5 * time.Millisecond)
	_, err = r.life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdResume, Ref: lifecycle.Ref{}, ManualRelease: true})
	require.NoError(t, err)

	require.NotEmpty(t, r.catchupPrecondition("ftask_a"))
	require.False(t, r.catchupGate("ftask_a"), "恢复之后还没同步过目录：追赶不写")
	require.NotEmpty(t, r.lifecycleStatus().CatchupBlocked)
	select {
	case <-r.syncer.kick:
	default:
		t.Fatal("恢复时应请求一次目录同步")
	}

	// 恢复之后同步成功（未变化也算）：放行追赶。
	time.Sleep(5 * time.Millisecond)
	_, err = r.catalog.Apply(CatalogSnapshot{NotModified: true})
	require.NoError(t, err)
	require.Empty(t, r.catchupPrecondition("ftask_a"))
	require.True(t, r.catchupGate("ftask_a"))
	require.Empty(t, r.lifecycleStatus().CatchupBlocked)
}

// 上报的状态与未暂停时：没有收敛证据。
func TestLifecycleStatusWithoutPauseIsNotQuiesced(t *testing.T) {
	r, _ := newLifecycleRuntime(t)
	startRunningModule(t, r)
	st := r.lifecycleStatus()
	require.False(t, st.Quiesced)
	require.True(t, st.PausedSince.IsZero())
	require.Equal(t, domain.EngineLifecycle{Stage: st.Stage}, st)
}
