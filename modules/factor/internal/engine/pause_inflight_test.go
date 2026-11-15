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

// startRunningModule 让测试运行时的任务能计算：窗口载入、检查通过（模块本来就在运行）。
func startRunningModule(t *testing.T, r *Runtime) {
	t.Helper()
	r.writes = newInflight()
	require.NoError(t, r.preload.PrepareSet(context.Background(), "ftask_a"))
	require.True(t, r.gate("ftask_a"))
}

// 暂停等在途的写结果操作结束才算收敛：登记之后的写操作没有结束时，Quiesce 一直等；结束后才写入收敛证据。
func TestPauseWaitsForRegisteredWritesBeforeQuiesced(t *testing.T) {
	r, _ := newLifecycleRuntime(t)
	startRunningModule(t, r)
	ctx := context.Background()

	release, ok := r.beginWrite(ctx, "ftask_a", true, false)
	require.True(t, ok)

	done := make(chan lifecycle.State, 1)
	go func() {
		state, _ := r.life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdPause})
		done <- state
	}()
	require.Eventually(t, func() bool { return !r.life.Running() }, 5*time.Second, 5*time.Millisecond)
	select {
	case <-done:
		t.Fatal("在途操作还没结束，暂停不应返回收敛")
	case <-time.After(100 * time.Millisecond):
	}
	require.False(t, r.life.State().Quiesced())

	release()
	select {
	case state := <-done:
		require.True(t, state.Quiesced())
	case <-time.After(5 * time.Second):
		t.Fatal("在途操作结束后暂停应当收敛")
	}
	require.True(t, r.lifecycleStatus().Quiesced)
	require.Equal(t, "paused", r.lifecycleStatus().Stage)
}

// 暂停之后：新的写操作被拒绝且不留在途登记；不领取补算。
func TestPausedModuleRejectsWritesAndLeases(t *testing.T) {
	r, _ := newLifecycleRuntime(t)
	startRunningModule(t, r)
	ctx := context.Background()
	_, err := r.life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdPause})
	require.NoError(t, err)

	_, ok := r.beginWrite(ctx, "ftask_a", true, true)
	require.False(t, ok)
	require.Zero(t, r.writes.count(""), "被拒绝的操作不留登记")
	require.False(t, r.leaseAllowed())

	client := &jobClientFake{job: testJob("BTC"), found: true}
	loop := &recalcLoop{client: client, executor: recalcexec.NewExecutor(&planRecorder{}), source: &sourceFake{}, poll: time.Millisecond,
		leaseAllowed: r.leaseAllowed, moduleOpen: r.moduleRunning, pulling: func() func() { return r.writes.enter("") }}
	found, err := loop.RunOnce(ctx)
	require.NoError(t, err)
	require.False(t, found)
	require.Zero(t, client.pulled, "暂停期间不领取补算")
}

// 模块暂停时补算作业立刻放弃并归还，不占着作业等门禁重开（放行后重新领取）；任务级的门禁等待仍然保留。
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
		leaseAllowed: r.leaseAllowed, moduleOpen: r.moduleRunning, pulling: func() func() { return r.writes.enter("") }}

	// 第一块跑完后暂停模块，再下一块被门禁挡住；作业不能等到“门禁重开”。
	var chunks int
	pauseDone := make(chan struct{})
	loop.executor = recalcexec.NewExecutor(&hookRunner{inner: runner, after: func() {
		chunks++
		if chunks == 1 {
			// 暂停命令要等这一块的在途登记结束才返回，所以放在后台，只等它先把门禁关上。
			go func() {
				defer close(pauseDone)
				_, _ = r.life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdPause})
			}()
			require.Eventually(t, func() bool { return !r.life.Running() }, 5*time.Second, time.Millisecond)
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
	statuses := reportedStatuses(client)
	require.Equal(t, jobStatusAccepted, statuses[len(statuses)-1], "放弃的作业要归还，不等作业租约过期")
	<-pauseDone // 暂停命令还会写生命周期状态文件：等它结束，临时目录才能干净清理
}

// 暂停前领取的作业还在补历史（不在在途登记里）时模块暂停、收敛、恢复进入追赶：追赶门禁放行分块，
// 但恢复追赶期间不续跑补算作业，补历史结束后作业直接归还，一个分块都不写。
func TestJobPulledBeforePauseDoesNotContinueDuringCatchup(t *testing.T) {
	r, _ := newLifecycleRuntime(t)
	startRunningModule(t, r)
	ctx := context.Background()
	client := &jobClientFake{job: testJob("BTC"), found: true}
	runner := &planRecorder{out: pipeline.Outcome{Status: "complete"}}
	executor := recalcexec.NewExecutor(runner, recalcexec.WithChunkPeriods(2), recalcexec.WithChunkRetry(1, time.Millisecond),
		recalcexec.WithWriteScope(func(ctx context.Context, taskID string) (func(), bool) {
			return r.beginWrite(ctx, taskID, true, true)
		}))
	inHistory, unblock := make(chan struct{}), make(chan struct{})
	loop := &recalcLoop{client: client, executor: executor, source: &sourceFake{}, poll: time.Millisecond,
		gateWait: 5 * time.Second, gateRetry: time.Millisecond,
		leaseAllowed: r.leaseAllowed, moduleOpen: r.moduleRunning, pulling: func() func() { return r.writes.enter("") },
		history: func(context.Context, domain.EngineTask, []string, int) error {
			close(inHistory)
			<-unblock
			return nil
		}}
	type outcome struct {
		found bool
		err   error
	}
	result := make(chan outcome, 1)
	go func() {
		found, err := loop.RunOnce(ctx)
		result <- outcome{found, err}
	}()
	<-inHistory
	state, err := r.life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdPause})
	require.NoError(t, err)
	require.True(t, state.Quiesced(), "补历史不在在途登记里，暂停立即收敛")
	_, err = r.life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdResume, ManualRelease: true})
	require.NoError(t, err)
	require.True(t, r.catchupGate("ftask_a"), "追赶门禁放行分块")
	require.False(t, r.leaseAllowed())

	started := time.Now()
	close(unblock)
	got := <-result
	require.NoError(t, got.err)
	require.False(t, got.found)
	require.Less(t, time.Since(started), 2*time.Second, "不等门禁重开")
	require.Empty(t, runner.plans, "恢复追赶期间不续跑暂停前领取的作业")
	require.Equal(t, []string{jobStatusAccepted}, reportedStatuses(client), "作业被归还")
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

// 追赶前置条件：恢复开始之后**发出**的目录同步请求成功过才允许追赶写；暂停前发出、恢复后才到达的旧响应不算。
func TestCatchupWaitsForACatalogSyncAfterResume(t *testing.T) {
	r, _ := newLifecycleRuntime(t)
	startRunningModule(t, r)
	ctx := context.Background()
	client := &gatedCatalogClient{release: make(chan struct{}), started: make(chan struct{}, 2)}
	r.syncer = &catalogSyncer{client: client, cache: r.catalog, kick: make(chan struct{}, 1), now: time.Now}

	// 暂停前发出一个请求，响应被卡住。
	oldDone := make(chan error, 1)
	go func() { oldDone <- r.syncer.SyncOnce(ctx) }()
	<-client.started

	_, err := r.life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdPause})
	require.NoError(t, err)
	time.Sleep(5 * time.Millisecond)
	_, err = r.life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdResume, ManualRelease: true})
	require.NoError(t, err)

	// 旧响应在恢复之后才到达并被应用：不能满足前置条件。
	client.release <- struct{}{}
	require.NoError(t, <-oldDone)
	require.NotEmpty(t, r.catchupPrecondition("ftask_a"), "恢复前发出的请求，响应晚到也不算")
	require.False(t, r.catchupGate("ftask_a"))
	require.NotEmpty(t, r.lifecycleStatus().CatchupBlocked)
	select {
	case <-r.syncer.kick:
	default:
		t.Fatal("恢复时应请求一次目录同步")
	}

	// 恢复之后发出的请求成功：放行追赶。
	time.Sleep(5 * time.Millisecond)
	go func() { client.release <- struct{}{} }()
	require.NoError(t, r.syncer.SyncOnce(ctx))
	require.Empty(t, r.catchupPrecondition("ftask_a"))
	require.True(t, r.catchupGate("ftask_a"))
	require.Empty(t, r.lifecycleStatus().CatchupBlocked)
}

// gatedCatalogClient 的 SyncCatalog 发出请求后等 release，用来构造“响应晚到”。
type gatedCatalogClient struct {
	release chan struct{}
	started chan struct{}
}

func (g *gatedCatalogClient) SyncCatalog(ctx context.Context, _ string) (CatalogSnapshot, error) {
	g.started <- struct{}{}
	select {
	case <-g.release:
	case <-ctx.Done():
		return CatalogSnapshot{}, ctx.Err()
	}
	return CatalogSnapshot{NotModified: true}, nil
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
