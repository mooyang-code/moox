package engine

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/recalcexec"
	"github.com/stretchr/testify/require"
)

type heartbeatClientFake struct {
	errs     []error
	statuses []domain.EngineStatus
}

func (f *heartbeatClientFake) Heartbeat(_ context.Context, status domain.EngineStatus) (time.Duration, error) {
	f.statuses = append(f.statuses, status)
	err := f.errs[0]
	f.errs = f.errs[1:]
	return 45 * time.Second, err
}

func TestHeartbeatLeaseConflictStopsAndRecovers(t *testing.T) {
	lease := &leaseState{}
	changes := 0
	client := &heartbeatClientFake{errs: []error{
		nil, fmt.Errorf("%w: held by factor-engine@other", ErrLeaseConflict), errors.New("dial tcp: timeout"), nil,
	}}
	beat := &heartbeater{client: client, lease: lease, status: func() domain.EngineStatus {
		return domain.EngineStatus{CatalogHash: "hash-1", ComputeWorkers: 8}
	}, onChange: func() { changes++ }, now: time.Now}

	require.NoError(t, beat.BeatOnce(context.Background()))
	require.False(t, lease.Conflict())

	require.Error(t, beat.BeatOnce(context.Background()))
	require.True(t, lease.Conflict(), "an explicit conflict stops the engine")

	require.Error(t, beat.BeatOnce(context.Background()))
	require.True(t, lease.Conflict(), "an unreachable manager keeps the last lease state")

	require.NoError(t, beat.BeatOnce(context.Background()))
	require.False(t, lease.Conflict())
	require.Equal(t, 2, changes)
	require.Equal(t, "hash-1", client.statuses[0].CatalogHash)
}

func TestHeartbeatUnreachableManagerKeepsRunning(t *testing.T) {
	lease := &leaseState{}
	beat := &heartbeater{client: &heartbeatClientFake{errs: []error{errors.New("connection refused")}}, lease: lease,
		status: func() domain.EngineStatus { return domain.EngineStatus{} }, now: time.Now}

	require.Error(t, beat.BeatOnce(context.Background()))

	require.False(t, lease.Conflict())
	require.Equal(t, "connection refused", lease.snapshot().LastError)
}

type reportCall struct {
	progress      time.Time
	status        string
	errText       string
	defaultFilled map[string][]domain.TimeRange
}

type jobClientFake struct {
	job      PulledJob
	found    bool
	pulled   int
	reports  []reportCall
	statuses []string
	err      error
}

func (f *jobClientFake) PullRecalcJob(context.Context) (PulledJob, bool, error) {
	f.pulled++
	found := f.found
	f.found = false
	return f.job, found, nil
}

func (f *jobClientFake) ReportRecalcProgress(_ context.Context, _ string, progress time.Time, status, errText string, defaultFilled map[string][]domain.TimeRange) (string, error) {
	f.reports = append(f.reports, reportCall{progress: progress, status: status, errText: errText, defaultFilled: defaultFilled})
	if f.err != nil {
		return "", f.err
	}
	if len(f.statuses) > 0 {
		next := f.statuses[0]
		f.statuses = f.statuses[1:]
		return next, nil
	}
	return status, nil
}

type sourceFake struct {
	subjects []string
	listed   int
}

func (f *sourceFake) DatasetColumns(context.Context, string, string) ([]string, error) {
	return []string{"close"}, nil
}

func (f *sourceFake) ListDatasetSubjects(context.Context, string, string) ([]string, error) {
	f.listed++
	return f.subjects, nil
}

type planRecorder struct {
	plans []pipeline.Plan
	err   error
	out   pipeline.Outcome
}

func (r *planRecorder) Run(_ context.Context, plan pipeline.Plan) (pipeline.Outcome, error) {
	r.plans = append(r.plans, plan)
	return r.out, r.err
}

func testJob(subjects ...string) PulledJob {
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	return PulledJob{
		JobID: "job-1", Subjects: subjects,
		Window: recalcexec.Window{Start: start, End: start.Add(6 * time.Minute), Progress: start},
		Set:    engineTask("ftask_a", "dataset_a", true, "bias"),
	}
}

func newTestLoop(client *jobClientFake, runner *planRecorder, source *sourceFake) *recalcLoop {
	executor := recalcexec.NewExecutor(runner, recalcexec.WithChunkPeriods(2), recalcexec.WithChunkRetry(1, time.Millisecond))
	return &recalcLoop{client: client, executor: executor, source: source, poll: time.Millisecond}
}

func TestRecalcLoopPullsRunsAndReportsSucceeded(t *testing.T) {
	client := &jobClientFake{job: testJob("BTC"), found: true}
	runner := &planRecorder{out: pipeline.Outcome{Status: "complete"}}
	loop := newTestLoop(client, runner, &sourceFake{})

	found, err := loop.RunOnce(context.Background())

	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, runner.plans, 3)
	require.Len(t, client.reports, 3)
	require.Equal(t, jobStatusRunning, client.reports[0].status)
	require.Equal(t, jobStatusSucceeded, client.reports[2].status)
	require.Equal(t, client.job.Window.End, client.reports[2].progress)
	require.Equal(t, []string{"BTC"}, runner.plans[0].Expected)
}

func TestRecalcLoopStopsOnCancelled(t *testing.T) {
	client := &jobClientFake{job: testJob("BTC"), found: true, statuses: []string{"cancelled"}}
	runner := &planRecorder{out: pipeline.Outcome{Status: "complete"}}
	loop := newTestLoop(client, runner, &sourceFake{})

	_, err := loop.RunOnce(context.Background())

	require.NoError(t, err)
	require.Len(t, runner.plans, 1)
	require.Len(t, client.reports, 1)
}

func TestRecalcLoopAbandonsOnConflict(t *testing.T) {
	client := &jobClientFake{job: testJob("BTC"), found: true, err: fmt.Errorf("%w: stale token", ErrLeaseConflict)}
	runner := &planRecorder{out: pipeline.Outcome{Status: "complete"}}
	loop := newTestLoop(client, runner, &sourceFake{})

	found, err := loop.RunOnce(context.Background())

	require.NoError(t, err)
	require.False(t, found, "放弃的作业按没有作业处理，等一个轮询间隔再领")
	require.Len(t, client.reports, 1, "no failed report after losing the registration")
}

func TestRecalcLoopAbandonsWhenManagerUnreachable(t *testing.T) {
	client := &jobClientFake{job: testJob("BTC"), found: true, err: errors.New("dial tcp: i/o timeout")}
	loop := newTestLoop(client, &planRecorder{out: pipeline.Outcome{Status: "complete"}}, &sourceFake{})

	found, err := loop.RunOnce(context.Background())

	require.NoError(t, err)
	require.False(t, found, "放弃的作业按没有作业处理，等一个轮询间隔再领")
	require.Len(t, client.reports, 1, "the job stays running and resumes on the next pull, never marked failed")
}

func TestRecalcLoopReportsFailedWithError(t *testing.T) {
	client := &jobClientFake{job: testJob("BTC"), found: true}
	runner := &planRecorder{err: errors.New("factor compute failed")}
	loop := newTestLoop(client, runner, &sourceFake{})

	_, err := loop.RunOnce(context.Background())

	require.NoError(t, err)
	require.Len(t, client.reports, 1)
	require.Equal(t, jobStatusFailed, client.reports[0].status)
	require.Contains(t, client.reports[0].errText, "factor compute failed")
}

func TestRecalcLoopResolvesSubjectsWhenEmpty(t *testing.T) {
	client := &jobClientFake{job: testJob(), found: true}
	runner := &planRecorder{out: pipeline.Outcome{Status: "complete"}}
	source := &sourceFake{subjects: []string{"BTC", "ETH"}}
	loop := newTestLoop(client, runner, source)

	_, err := loop.RunOnce(context.Background())

	require.NoError(t, err)
	require.Positive(t, source.listed)
	require.Equal(t, []string{"BTC", "ETH"}, runner.plans[0].Expected)
}

func TestRecalcLoopKeepsDegradedNote(t *testing.T) {
	client := &jobClientFake{job: testJob("BTC", "ETH"), found: true}
	runner := &planRecorder{out: pipeline.Outcome{Status: "degraded", FailedSubjects: []string{"ETH"}}}
	loop := newTestLoop(client, runner, &sourceFake{})

	_, err := loop.RunOnce(context.Background())

	require.NoError(t, err)
	require.Equal(t, "degraded: failed_subjects=1", client.reports[2].errText)
}

func TestRecalcLoopPausesWhileInactive(t *testing.T) {
	client := &jobClientFake{job: testJob("BTC"), found: true}
	loop := newTestLoop(client, &planRecorder{}, &sourceFake{})
	loop.active = func() bool { return false }

	found, err := loop.RunOnce(context.Background())

	require.NoError(t, err)
	require.False(t, found)
	require.Zero(t, client.pulled)
}

func TestRecalcLoopReportsFailedWithLatestProgress(t *testing.T) {
	calls := 0
	runner := &planRecorder{}
	client := &jobClientFake{job: testJob("BTC"), found: true}
	loop := newTestLoop(client, runner, &sourceFake{})
	loop.executor = recalcexec.NewExecutor(runnerFunc(func(context.Context, pipeline.Plan) (pipeline.Outcome, error) {
		calls++
		if calls > 1 {
			return pipeline.Outcome{}, errors.New("factor compute failed")
		}
		return pipeline.Outcome{Status: "complete"}, nil
	}), recalcexec.WithChunkPeriods(2), recalcexec.WithChunkRetry(1, time.Millisecond))

	_, err := loop.RunOnce(context.Background())

	require.NoError(t, err)
	require.Len(t, client.reports, 2)
	require.Equal(t, jobStatusFailed, client.reports[1].status)
	require.Equal(t, client.reports[0].progress, client.reports[1].progress, "the failure carries the progress already reported")
}

func TestRecalcLoopStopsWhenLeaseLost(t *testing.T) {
	client := &jobClientFake{job: testJob("BTC"), found: true}
	runner := &planRecorder{out: pipeline.Outcome{Status: "complete"}}
	loop := newTestLoop(client, runner, &sourceFake{})
	pulled := false
	loop.active = func() bool {
		if !pulled {
			pulled = true
			return true
		}
		return false
	}

	_, err := loop.RunOnce(context.Background())

	require.NoError(t, err)
	require.Len(t, runner.plans, 1, "the job stops at the first chunk boundary after losing the lease")
}

type runnerFunc func(context.Context, pipeline.Plan) (pipeline.Outcome, error)

func (f runnerFunc) Run(ctx context.Context, plan pipeline.Plan) (pipeline.Outcome, error) {
	return f(ctx, plan)
}

// 补算作业自带定义（JSON，没有编译结果），领取时现场编译后再交给流水线。
func TestRecalcLoopCompilesJobDefinitions(t *testing.T) {
	job := testJob("BTC")
	job.Set = uncompiled(job.Set)
	require.Nil(t, job.Set.Members[0].Factor.Compiled())
	client := &jobClientFake{job: job, found: true}
	runner := &planRecorder{out: pipeline.Outcome{Status: "complete"}}
	loop := newTestLoop(client, runner, &sourceFake{})

	_, err := loop.RunOnce(context.Background())

	require.NoError(t, err)
	require.Len(t, runner.plans, 3)
	for _, plan := range runner.plans {
		require.NotNil(t, plan.Members[0].Factor.Compiled())
		require.Equal(t, 2, plan.Members[0].Factor.LookbackPeriods)
	}
}

// beatRecorder 记录每次心跳，供并发的心跳循环测试使用。
type beatRecorder struct{ beats chan domain.EngineStatus }

func (r *beatRecorder) Heartbeat(_ context.Context, status domain.EngineStatus) (time.Duration, error) {
	r.beats <- status
	return 45 * time.Second, nil
}

// 每算完一期就唤醒心跳循环：不等 10 秒的定时器，立刻把这一期的完成时刻报给因子管理服务。
func TestHeartbeatWakeSendsWithoutWaitingForTheTicker(t *testing.T) {
	recorder := &beatRecorder{beats: make(chan domain.EngineStatus, 4)}
	beat := &heartbeater{client: recorder, lease: &leaseState{}, status: func() domain.EngineStatus { return domain.EngineStatus{} },
		interval: time.Hour, now: time.Now, wake: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go beat.Run(ctx)

	runs := newRunTracker()
	runs.setOnRecord(beat.Wake)
	period := time.Date(2026, 10, 16, 15, 45, 0, 0, time.UTC)
	runs.record("task_a", testAxis("1m"), period, "complete", pipeline.Outcome{}, period.Add(66*time.Second))
	select {
	case <-recorder.beats:
	case <-time.After(5 * time.Second):
		t.Fatal("recording a period must send a heartbeat right away")
	}
	// 比已记录的更早的一期不覆盖，也不唤醒。
	runs.record("task_a", testAxis("1m"), period.Add(-time.Minute), "complete", pipeline.Outcome{}, period)
	select {
	case <-recorder.beats:
		t.Fatal("an older period must not wake the heartbeat")
	case <-time.After(200 * time.Millisecond):
	}
	// 多次唤醒在发出前合并成一次。
	beat.Wake()
	beat.Wake()
	(&heartbeater{}).Wake()
}

// 补算前先把本地历史延长到覆盖「区间起点 − 回看」：区间早于本地最早数据时，否则整段按失败记录；补历史失败不终止任务。
func TestRecalcLoopExtendsLocalHistoryBeforeRunning(t *testing.T) {
	client := &jobClientFake{job: testJob("BTC"), found: true}
	runner := &planRecorder{out: pipeline.Outcome{Status: "complete"}}
	loop := newTestLoop(client, runner, &sourceFake{})
	loop.now = func() time.Time { return client.job.Window.Start.Add(100 * time.Minute) }
	var gotBars int
	var gotSubjects []string
	loop.history = func(_ context.Context, set domain.EngineTask, subjects []string, bars int) error {
		gotBars, gotSubjects = bars, subjects
		return nil
	}

	found, err := loop.RunOnce(context.Background())

	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, 100+maxLookbackOf(client.job.Set)+1, gotBars)
	require.Equal(t, []string{"BTC"}, gotSubjects)
	require.Len(t, runner.plans, 3)

	// 补历史失败：任务照常运行（只用已有的本地数据）。
	failing := &jobClientFake{job: testJob("BTC"), found: true}
	loop2 := newTestLoop(failing, &planRecorder{out: pipeline.Outcome{Status: "complete"}}, &sourceFake{})
	loop2.history = func(context.Context, domain.EngineTask, []string, int) error { return errors.New("storage 不可达") }
	_, err = loop2.RunOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, jobStatusSucceeded, failing.reports[len(failing.reports)-1].status)
}

// 任务刚建立、检查还没通过时任务会被门禁挡住：在限定时间内重试，放行后继续跑完，
// 一直挡住时归还作业（回到待领取，不等作业租约过期），不报失败。
func TestRecalcLoopWaitsForTheLifecycleGateToOpen(t *testing.T) {
	var attempts int
	executor := func(runner *planRecorder) *recalcexec.Executor {
		return recalcexec.NewExecutor(runner, recalcexec.WithChunkPeriods(2), recalcexec.WithChunkRetry(1, time.Millisecond),
			recalcexec.WithGate(func(string) bool { attempts++; return attempts > 2 }))
	}
	client := &jobClientFake{job: testJob("BTC"), found: true}
	runner := &planRecorder{out: pipeline.Outcome{Status: "complete"}}
	loop := &recalcLoop{client: client, executor: executor(runner), source: &sourceFake{}, poll: time.Millisecond,
		gateWait: time.Second, gateRetry: time.Millisecond}

	found, err := loop.RunOnce(context.Background())

	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, runner.plans, 3, "放行后补算跑完")
	require.Equal(t, jobStatusSucceeded, client.reports[len(client.reports)-1].status)

	// 一直被挡住：超过等待时间后归还作业、按没有任务处理，不报失败。
	client2 := &jobClientFake{job: testJob("BTC"), found: true}
	runner2 := &planRecorder{out: pipeline.Outcome{Status: "complete"}}
	blocked := recalcexec.NewExecutor(runner2, recalcexec.WithGate(func(string) bool { return false }))
	loop2 := &recalcLoop{client: client2, executor: blocked, source: &sourceFake{}, poll: time.Millisecond, gateWait: 20 * time.Millisecond, gateRetry: 5 * time.Millisecond}
	found, err = loop2.RunOnce(context.Background())
	require.NoError(t, err)
	require.False(t, found)
	require.Empty(t, runner2.plans)
	require.Equal(t, []string{jobStatusAccepted}, reportedStatuses(client2), "被门禁挡住只归还作业，不报失败")
}

// 停止时正在上报的分块因上下文取消而失败：仍按引擎停止处理，归还作业。
func TestRecalcLoopGivesTheJobBackWhenTheReportFailsOnShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := &jobClientFake{job: testJob("BTC"), found: true}
	runner := &planRecorder{out: pipeline.Outcome{Status: "complete"}}
	stopping := recalcexec.NewExecutor(&hookRunner{inner: runner, after: func() { client.err = context.Canceled; cancel() }},
		recalcexec.WithChunkPeriods(2), recalcexec.WithChunkRetry(1, time.Millisecond))
	loop := &recalcLoop{client: client, executor: stopping, source: &sourceFake{}, poll: time.Millisecond}

	_, err := loop.RunOnce(ctx)

	require.NoError(t, err)
	statuses := reportedStatuses(client)
	require.Equal(t, jobStatusAccepted, statuses[len(statuses)-1])
}

// 引擎停止时在跑的作业被归还：重启后立刻能重新领取，不等作业租约过期。
func TestRecalcLoopGivesTheJobBackOnShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := &jobClientFake{job: testJob("BTC"), found: true}
	runner := &planRecorder{out: pipeline.Outcome{Status: "complete"}}
	stopping := recalcexec.NewExecutor(&hookRunner{inner: runner, after: cancel}, recalcexec.WithChunkPeriods(2), recalcexec.WithChunkRetry(1, time.Millisecond))
	loop := &recalcLoop{client: client, executor: stopping, source: &sourceFake{}, poll: time.Millisecond}

	_, err := loop.RunOnce(ctx)

	require.NoError(t, err)
	statuses := reportedStatuses(client)
	require.NotEmpty(t, statuses)
	require.Equal(t, jobStatusAccepted, statuses[len(statuses)-1])
	require.NotContains(t, statuses, jobStatusFailed)
}

// 补算在算第一个周期之前先把本地数据拉到作业区间的终点：Storage 里比本地更新的周期（例如强制补采写入的）才读得到。
// 拉取失败不终止作业。
func TestRecalcLoopCatchesUpLocalDataBeforeComputing(t *testing.T) {
	client := &jobClientFake{job: testJob("BTC"), found: true}
	runner := &planRecorder{out: pipeline.Outcome{Status: "complete"}}
	loop := newTestLoop(client, runner, &sourceFake{})
	var ends []time.Time
	var plansAtCatchUp int
	loop.catchUp = func(_ context.Context, set domain.EngineTask, subjects []string, end time.Time) (int, error) {
		require.Equal(t, "ftask_a", set.Set.TaskID)
		require.Equal(t, []string{"BTC"}, subjects)
		ends, plansAtCatchUp = append(ends, end), len(runner.plans)
		return 1, nil
	}
	found, err := loop.RunOnce(context.Background())
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, []time.Time{client.job.Window.End}, ends)
	require.Zero(t, plansAtCatchUp, "要在第一个周期开始计算之前拉")
	require.Len(t, runner.plans, 3)

	failing := &jobClientFake{job: testJob("BTC"), found: true}
	runner2 := &planRecorder{out: pipeline.Outcome{Status: "complete"}}
	loop2 := newTestLoop(failing, runner2, &sourceFake{})
	loop2.catchUp = func(context.Context, domain.EngineTask, []string, time.Time) (int, error) {
		return 0, errors.New("storage unavailable")
	}
	found, err = loop2.RunOnce(context.Background())
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, jobStatusSucceeded, failing.reports[len(failing.reports)-1].status)
}

// 股票日历（有午休/隔夜/长假间隙）在未推进任何分块前失败或归还时，必须直接回传领取时的原始真实时间 Progress，
// 不走 EndToAxis → FromAxisEnd 往返；否则开盘时刻会倒退到上一交易日收盘时刻，在管理端触发 ErrConflict 永久卡死单槽队列。
func TestRecalcLoopKeepsOriginalProgressBeforeFirstChunkOnSessionCalendar(t *testing.T) {
	cst := time.FixedZone("CST", 8*3600)
	// 2026-10-08 09:30（国庆后首个交易日开盘）→ 10:00；轴往返会倒退到 2026-09-30 15:00。
	start := time.Date(2026, 10, 8, 9, 30, 0, 0, cst).UTC()
	end := time.Date(2026, 10, 8, 10, 0, 0, 0, cst).UTC()
	newStockJob := func() PulledJob {
		set := engineTask("ftask_stock", "dataset_a", true, "bias")
		set.Set.SpaceID = "stockcn"
		set.Set.Freq = "5m"
		return PulledJob{
			JobID: "job-stock", Subjects: []string{"000001.XSHE"},
			Window: recalcexec.Window{Start: start, End: end, Progress: start},
			Set:    set,
		}
	}

	// 1. 首块执行失败：上报 failed 且 progress 精确等于领取时的原始真实时间 start。
	client := &jobClientFake{job: newStockJob(), found: true}
	runner := &planRecorder{err: errors.New("storage read failed")}
	loop := newTestLoop(client, runner, &sourceFake{})
	found, err := loop.RunOnce(context.Background())
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, client.reports, 1)
	require.Equal(t, jobStatusFailed, client.reports[0].status)
	require.True(t, start.Equal(client.reports[0].progress), "首块失败应回传原始开盘时间 %s，实际 %s", start, client.reports[0].progress)

	// 2. prepare 在首块前失败：同样原样回传 start。
	prepClient := &jobClientFake{job: newStockJob(), found: true}
	prepLoop := newTestLoop(prepClient, &planRecorder{}, &sourceFake{})
	prepLoop.prepare = func(context.Context, domain.EngineTask) (func(), error) {
		return nil, errors.New("udf load failed")
	}
	found, err = prepLoop.RunOnce(context.Background())
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, prepClient.reports, 1)
	require.Equal(t, jobStatusFailed, prepClient.reports[0].status)
	require.True(t, start.Equal(prepClient.reports[0].progress))

	// 3. 首块前被门禁挡住归还（giveBack）：原样回传 start。
	gatedClient := &jobClientFake{job: newStockJob(), found: true}
	gatedExec := recalcexec.NewExecutor(&planRecorder{}, recalcexec.WithGate(func(string) bool { return false }))
	gatedLoop := &recalcLoop{client: gatedClient, executor: gatedExec, source: &sourceFake{}, poll: time.Millisecond, gateWait: 10 * time.Millisecond, gateRetry: 2 * time.Millisecond}
	found, err = gatedLoop.RunOnce(context.Background())
	require.NoError(t, err)
	require.False(t, found)
	require.Len(t, gatedClient.reports, 1)
	require.Equal(t, jobStatusAccepted, gatedClient.reports[0].status)
	require.True(t, start.Equal(gatedClient.reports[0].progress))

	// 4. 推进完第一块（2 根 5m 线：09:30、09:35，收盘 09:40）后第二块失败：回传第一块已确认的真实进度 09:40。
	chunkClient := &jobClientFake{job: newStockJob(), found: true}
	calls := 0
	chunkLoop := newTestLoop(chunkClient, &planRecorder{}, &sourceFake{})
	chunkLoop.executor = recalcexec.NewExecutor(runnerFunc(func(context.Context, pipeline.Plan) (pipeline.Outcome, error) {
		calls++
		if calls > 1 {
			return pipeline.Outcome{}, errors.New("second chunk failed")
		}
		return pipeline.Outcome{Status: "complete"}, nil
	}), recalcexec.WithChunkPeriods(2), recalcexec.WithChunkRetry(1, time.Millisecond))
	_, err = chunkLoop.RunOnce(context.Background())
	require.NoError(t, err)
	require.Len(t, chunkClient.reports, 2)
	wantFirstChunkEnd := time.Date(2026, 10, 8, 9, 40, 0, 0, cst).UTC()
	require.Equal(t, jobStatusRunning, chunkClient.reports[0].status)
	require.True(t, wantFirstChunkEnd.Equal(chunkClient.reports[0].progress))
	require.Equal(t, jobStatusFailed, chunkClient.reports[1].status)
	require.True(t, wantFirstChunkEnd.Equal(chunkClient.reports[1].progress))
}
