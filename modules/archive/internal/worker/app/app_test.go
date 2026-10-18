package app

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/worker/config"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/export"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/manager"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/runner"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/source"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/staging"
	archivepb "github.com/mooyang-code/moox/modules/archive/proto/archivegen"
)

type fakeManager struct {
	mu            sync.Mutex
	heartbeat     *archivepb.ArchiveWorkerHeartbeatRsp
	heartbeatErr  error
	assignments   []*archivepb.RunAssignment
	pullErr       error
	progressStop  bool
	completeErrs  []error
	heartbeats    []*archivepb.ArchiveWorkerHeartbeatReq
	pulls         []*archivepb.PullArchiveRunReq
	progresses    []*archivepb.ReportArchiveRunProgressReq
	completes     []*archivepb.CompleteArchiveRunReq
	completeCalls int
	completed     chan struct{}
	pullGate      chan struct{} // 非空时 Pull 阻塞到它被关闭
	pullEntered   chan struct{} // Pull 进入时发出信号
}

func newFakeManager() *fakeManager {
	return &fakeManager{heartbeat: &archivepb.ArchiveWorkerHeartbeatRsp{Active: true, LeaseTtlSeconds: 45}, completed: make(chan struct{}, 16)}
}

func (f *fakeManager) Heartbeat(_ context.Context, req *archivepb.ArchiveWorkerHeartbeatReq) (*archivepb.ArchiveWorkerHeartbeatRsp, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heartbeats = append(f.heartbeats, req)
	return f.heartbeat, f.heartbeatErr
}

func (f *fakeManager) Pull(_ context.Context, req *archivepb.PullArchiveRunReq) (*archivepb.PullArchiveRunRsp, error) {
	f.mu.Lock()
	gate, entered := f.pullGate, f.pullEntered
	f.mu.Unlock()
	if entered != nil {
		close(entered)
	}
	if gate != nil {
		<-gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pulls = append(f.pulls, req)
	if f.pullErr != nil {
		return nil, f.pullErr
	}
	out := f.assignments
	f.assignments = nil
	return &archivepb.PullArchiveRunRsp{Runs: out}, nil
}

func (f *fakeManager) Progress(_ context.Context, req *archivepb.ReportArchiveRunProgressReq) (*archivepb.ReportArchiveRunProgressRsp, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.progresses = append(f.progresses, req)
	return &archivepb.ReportArchiveRunProgressRsp{Canceled: f.progressStop}, nil
}

func (f *fakeManager) Complete(_ context.Context, req *archivepb.CompleteArchiveRunReq) (*archivepb.CompleteArchiveRunRsp, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completeCalls++
	if len(f.completeErrs) > 0 {
		err := f.completeErrs[0]
		f.completeErrs = f.completeErrs[1:]
		if err != nil {
			return nil, err
		}
	}
	f.completes = append(f.completes, req)
	f.completed <- struct{}{}
	return &archivepb.CompleteArchiveRunRsp{}, nil
}

type fakeSource struct {
	mu     sync.Mutex
	rows   []staging.Row
	block  bool          // 读取一直阻塞到运行被取消
	delay  time.Duration // 每次读取的耗时
	active int
	maxAct int
	calls  int
}

func (f *fakeSource) Columns(context.Context, string, string) ([]staging.Column, error) {
	return []staging.Column{{Name: "close", Type: staging.TypeDouble}}, nil
}

func (f *fakeSource) ReadRange(ctx context.Context, req source.Range) (source.Page, error) {
	f.mu.Lock()
	f.active++
	f.calls++
	if f.active > f.maxAct {
		f.maxAct = f.active
	}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	}()
	if f.block {
		<-ctx.Done()
		return source.Page{}, ctx.Err()
	}
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	var out []staging.Row
	for _, row := range f.rows {
		if !row.DataTime.Before(req.Start) && row.DataTime.Before(req.End) {
			out = append(out, row)
		}
	}
	return source.Page{Rows: out, Bytes: int64(100 * len(out))}, nil
}

type instantPacer struct{}

func (instantPacer) WaitTurn(context.Context) error             { return nil }
func (instantPacer) Throttle(context.Context, int64) error      { return nil }
func (instantPacer) Sleep(context.Context, time.Duration) error { return nil }
func (instantPacer) Interval() time.Duration                    { return 0 }

type harness struct {
	t     *testing.T
	app   *App
	mgr   *fakeManager
	src   *fakeSource
	store *staging.Store
	root  string
	cfg   *config.Config
	now   time.Time
	free  uint64
	slept []time.Duration
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	root := t.TempDir()
	store, err := staging.Open(filepath.Join(root, ".state", "staging.db"), staging.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	h := &harness{t: t, mgr: newFakeManager(), src: &fakeSource{}, store: store, root: root, cfg: config.Default(), now: time.Date(2026, 10, 17, 1, 4, 43, 0, time.UTC), free: 100 << 30}
	h.cfg.Worker.WorkerID, h.cfg.Worker.RootDir = "w1", root
	files := export.Files{Root: root}
	r := &runner.Runner{Source: h.src, Staging: store, Files: files, Pace: instantPacer{}, PageRows: 2000, Now: func() time.Time { return h.now }, ReadAttempts: 1}
	h.app = New(Options{
		Config: h.cfg, Manager: h.mgr, Runner: r, Staging: store, Files: files, InstanceID: "i1", Version: "test",
		Now: func() time.Time { return h.now },
		Sleep: func(ctx context.Context, d time.Duration) error {
			h.slept = append(h.slept, d)
			h.now = h.now.Add(d)
			return ctx.Err()
		},
		DiskUsage: func(string) (uint64, uint64, error) { return 200 << 30, h.free, nil },
	})
	return h
}

func assignment(runID string, mutate func(*archivepb.RunAssignment)) *archivepb.RunAssignment {
	a := &archivepb.RunAssignment{
		RunId: runID, LeaseToken: "tok-" + runID, Kind: "scheduled", SpaceId: "crypto", DatasetId: "ds_1m", FilePrefix: "ds_1m", Freq: "1m", PartitionUnit: "day", DataNodeId: "node-0",
		Retention: "168h", IntervalSeconds: 3600, MinLookbackSeconds: 900, SpaceTz: "UTC", StagingRetentionSeconds: 72 * 3600,
		StartedAt: "2026-10-17T01:04:43Z", ScheduledAt: "2026-10-17T01:04:43Z",
	}
	if mutate != nil {
		mutate(a)
	}
	return a
}

func (h *harness) waitComplete() *archivepb.CompleteArchiveRunReq {
	h.t.Helper()
	select {
	case <-h.mgr.completed:
	case <-time.After(20 * time.Second):
		h.t.Fatal("等待 CompleteArchiveRun 超时")
	}
	h.mgr.mu.Lock()
	defer h.mgr.mu.Unlock()
	return h.mgr.completes[len(h.mgr.completes)-1]
}

func (h *harness) idle() {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for len(h.app.runningIDs()) > 0 {
		if time.Now().After(deadline) {
			h.t.Fatal("运行没有结束")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestHeartbeatReportsStateAndAppliesTheManagersInstructions(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	cancelled := false
	h.app.mu.Lock()
	h.app.running["r1"] = &activeRun{assignment: assignment("r1", nil), cancel: func() { cancelled = true }}
	h.app.mu.Unlock()
	h.mgr.heartbeat = &archivepb.ArchiveWorkerHeartbeatRsp{Active: true, LeaseTtlSeconds: 30, CancelRunIds: []string{"r1", "ghost"},
		DeletedTasks: []*archivepb.TaskRef{{SpaceId: "crypto", DatasetId: "old", TaskEpoch: "1"}}}
	h.app.heartbeatOnce(ctx)
	req := h.mgr.heartbeats[0]
	if req.GetWorkerId() != "w1" || req.GetInstanceId() != "i1" || req.GetVersion() != "test" || req.GetRootDir() != h.root || req.GetDiskTotalBytes() != 200<<30 ||
		req.GetDiskFreeBytes() != 100<<30 || req.GetDiskLow() || len(req.GetRunningRunIds()) != 1 || !strings.Contains(req.GetPacingJson(), "max_concurrent_runs") {
		t.Fatalf("heartbeat = %v", req)
	}
	if !cancelled || !h.app.running["r1"].stop.Load() {
		t.Fatal("mgr 要求取消的运行应被取消")
	}
	if len(h.app.deleted) != 1 || h.app.leaseTTL != 30*time.Second || !h.app.leaseValid() {
		t.Fatalf("deleted=%v ttl=%s", h.app.deleted, h.app.leaseTTL)
	}
	// 磁盘低于下限：心跳上报 disk_low。
	h.free = 1 << 30
	h.app.heartbeatOnce(ctx)
	if !h.mgr.heartbeats[1].GetDiskLow() {
		t.Fatal("可用空间低于 min_free_bytes 应上报 disk_low")
	}
}

func TestHeartbeatConflictAndFailuresStopPulling(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.app.heartbeatOnce(ctx)
	if !h.app.leaseValid() || !h.app.Snapshot().Ready {
		t.Fatal("首次心跳成功后应就绪")
	}
	h.mgr.heartbeatErr = errors.Join(manager.ErrConflict, errors.New("duplicate"))
	h.app.heartbeatOnce(ctx)
	if h.app.leaseValid() || h.app.Snapshot().Active || h.app.Snapshot().LastError == "" {
		t.Fatalf("冲突后应不活动: %+v", h.app.Snapshot())
	}
	// 普通失败：保留活动状态，但租约超过有效期后不再领取。
	h.mgr.heartbeatErr = nil
	h.app.heartbeatOnce(ctx)
	h.mgr.heartbeatErr = errors.New("timeout")
	h.app.heartbeatOnce(ctx)
	if !h.app.leaseValid() {
		t.Fatal("租约有效期内仍可领取")
	}
	h.now = h.now.Add(46 * time.Second)
	if h.app.leaseValid() {
		t.Fatal("超过租约有效期应停止领取")
	}
	if snap := h.app.Snapshot(); snap.Ready {
		t.Fatalf("心跳长时间失败应不就绪: %+v", snap)
	}
}

func TestPullHonorsSlotsLeaseMaintenanceAndDisk(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.cfg.Pacing.MaxConcurrentRuns, h.cfg.Pacing.MaxRunsPerDataNode = 3, 2
	h.app.pullOnce(ctx)
	if len(h.mgr.pulls) != 0 {
		t.Fatal("没有取得租约不领取")
	}
	h.app.heartbeatOnce(ctx)
	h.app.mu.Lock()
	h.app.running["a"] = &activeRun{assignment: assignment("a", nil), cancel: func() {}}
	h.app.running["b"] = &activeRun{assignment: assignment("b", func(x *archivepb.RunAssignment) { x.DataNodeId = "node-1" }), cancel: func() {}}
	h.app.mu.Unlock()
	h.app.pullOnce(ctx)
	if len(h.mgr.pulls) != 1 {
		t.Fatalf("pulls = %d", len(h.mgr.pulls))
	}
	req := h.mgr.pulls[0]
	loads := map[string]uint32{}
	for _, l := range req.GetBusyDataNodes() {
		loads[l.GetDataNodeId()] = l.GetRunning()
	}
	if req.GetFreeSlots() != 1 || req.GetMaxRunsPerDataNode() != 2 || req.GetWorkerId() != "w1" || req.GetInstanceId() != "i1" || loads["node-0"] != 1 || loads["node-1"] != 1 {
		t.Fatalf("pull = %v", req)
	}
	// 槽满：不拉取。
	h.app.mu.Lock()
	h.app.running["c"] = &activeRun{assignment: assignment("c", nil), cancel: func() {}}
	h.app.mu.Unlock()
	h.app.pullOnce(ctx)
	if len(h.mgr.pulls) != 1 {
		t.Fatal("槽满不应拉取")
	}
	h.app.mu.Lock()
	delete(h.app.running, "c")
	h.app.maintaining = true
	h.app.mu.Unlock()
	h.app.pullOnce(ctx)
	h.app.mu.Lock()
	h.app.maintaining, h.app.diskLow = false, true
	h.app.mu.Unlock()
	h.app.pullOnce(ctx)
	if len(h.mgr.pulls) != 1 {
		t.Fatalf("维护中或磁盘不足不应拉取: %d", len(h.mgr.pulls))
	}
	h.app.mu.Lock()
	h.app.diskLow = false
	h.app.mu.Unlock()
	h.mgr.pullErr = errors.New("mgr down")
	h.app.pullOnce(ctx) // 失败只记录日志
	if len(h.mgr.pulls) != 2 {
		t.Fatal("恢复后应再次拉取")
	}
}

func TestPulledRunsAreExecutedAndReportedWithStatsAndPartitions(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	day := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 50; i++ {
		h.src.rows = append(h.src.rows, staging.Row{SubjectID: "BTC", DataTime: day.Add(time.Duration(i) * time.Minute), Values: []any{float64(i)}})
	}
	h.mgr.assignments = []*archivepb.RunAssignment{assignment("r1", nil)}
	h.app.heartbeatOnce(ctx)
	h.app.pullOnce(ctx)
	req := h.waitComplete()
	if req.GetRunId() != "r1" || req.GetLeaseToken() != "tok-r1" || req.GetWorkerId() != "w1" || req.GetStatus() != "succeeded" ||
		req.GetRowsRead() != 50 || req.GetRowsAdded() != 50 || req.GetRequestCount() < 1 || req.GetSubjectsDone() != 1 {
		t.Fatalf("complete = %v", req)
	}
	if len(req.GetPartitions()) != 1 || req.GetPartitions()[0].GetPartitionKey() != "2026-10-17" || req.GetPartitions()[0].GetStatus() != "open" || req.GetPartitions()[0].GetRowCount() != 50 {
		t.Fatalf("partitions = %v", req.GetPartitions())
	}
	h.idle()
	// 同样的数据再跑一次：没有新增。
	h.mgr.assignments = []*archivepb.RunAssignment{assignment("r2", func(x *archivepb.RunAssignment) { x.StartedAt = "2026-10-17T02:04:43Z" })}
	h.now = h.now.Add(time.Hour)
	h.app.heartbeatOnce(ctx) // 一小时后租约需要靠新的心跳续上
	h.app.pullOnce(ctx)
	if second := h.waitComplete(); second.GetRunId() != "r2" || second.GetRowsAdded() != 0 || second.GetStatus() != "succeeded" {
		t.Fatalf("second = %v", second)
	}
}

func TestIndependentProgressTimerRenewsTheLeaseAndHonorsCancellation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.app.progressEvery = 10 * time.Millisecond
	h.src.block = true // 读取一直不返回：只有独立的续租定时器能让 mgr 知道运行还活着
	h.mgr.progressStop = true
	h.mgr.assignments = []*archivepb.RunAssignment{assignment("r1", nil)}
	h.app.heartbeatOnce(ctx)
	h.app.pullOnce(ctx)
	req := h.waitComplete()
	if req.GetStatus() != "canceled" {
		t.Fatalf("mgr 在进度上报里要求取消时应以 canceled 收尾: %v", req)
	}
	h.mgr.mu.Lock()
	defer h.mgr.mu.Unlock()
	if len(h.mgr.progresses) == 0 || h.mgr.progresses[0].GetLeaseToken() != "tok-r1" || h.mgr.progresses[0].GetWorkerId() != "w1" {
		t.Fatalf("progresses = %v", h.mgr.progresses)
	}
}

func TestRunsOfTheSameDatasetNeverOverlapLocally(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.app.progressEvery = 10 * time.Millisecond
	day := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		h.src.rows = append(h.src.rows, staging.Row{SubjectID: "BTC", DataTime: day.Add(time.Duration(i) * time.Minute), Values: []any{float64(i)}})
	}
	h.src.delay = 150 * time.Millisecond
	// mgr 取消旧运行后立刻为同一个数据集派发了新运行：两个运行同时在 worker 里，但必须一个接一个执行。
	h.mgr.assignments = []*archivepb.RunAssignment{assignment("old", nil), assignment("new", nil)}
	h.app.heartbeatOnce(ctx)
	h.app.pullOnce(ctx)
	h.waitComplete()
	h.waitComplete()
	h.idle()
	if h.src.maxAct != 1 {
		t.Fatalf("同一数据集的运行不能同时读取/写入暂存区: 最大并发 %d", h.src.maxAct)
	}
	if len(h.mgr.completes) != 2 {
		t.Fatalf("completes = %d", len(h.mgr.completes))
	}
	// 排队等待期间仍在续租。
	if len(h.mgr.progresses) == 0 {
		t.Fatal("排队的运行也要续租")
	}
}

func TestMaintenanceWaitsForInFlightPullsAndANewAssignmentCancelsAStaleDeletion(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for _, ds := range []string{"gone", "ds_1m"} {
		task := staging.Task{SpaceID: "crypto", DatasetID: ds, Columns: []staging.Column{{Name: "close", Type: staging.TypeDouble}}}
		if err := h.store.EnsureTable(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	h.mgr.heartbeat = &archivepb.ArchiveWorkerHeartbeatRsp{Active: true, LeaseTtlSeconds: 45, DeletedTasks: []*archivepb.TaskRef{{SpaceId: "crypto", DatasetId: "gone", TaskEpoch: "1"}, {SpaceId: "crypto", DatasetId: "ds_1m", TaskEpoch: "1"}}}
	h.app.heartbeatOnce(ctx)
	if len(h.app.deleted) != 2 {
		t.Fatalf("deleted = %v", h.app.deleted)
	}
	// 领取调用还在途中（运行还没登记）：维护不能开始，已删除的任务不能被清理。
	gate := make(chan struct{})
	entered := make(chan struct{})
	h.mgr.pullGate, h.mgr.pullEntered = gate, entered
	h.mgr.assignments = []*archivepb.RunAssignment{assignment("r1", nil)}
	pulled := make(chan struct{})
	go func() { h.app.pullOnce(ctx); close(pulled) }()
	<-entered
	h.app.maintainOnce(ctx)
	if tables, _ := h.store.StagedTables(ctx); len(tables) != 2 {
		t.Fatalf("在途领取期间维护必须等待（最终跳过），不能清理: %v", tables)
	}
	close(gate)
	<-pulled
	h.waitComplete()
	h.idle()
	// 新派发的运行属于「ds_1m」：它的删除通知作废；真正被删除的「gone」照常清理。
	h.app.maintainOnce(ctx)
	tables, _ := h.store.StagedTables(ctx)
	keep := staging.Task{SpaceID: "crypto", DatasetID: "ds_1m"}.Table()
	if len(tables) != 1 || tables[0] != keep {
		t.Fatalf("tables = %v, want only %s", tables, keep)
	}
}

func TestPartitionsAreMarkedReportedOnlyAfterTheManagerAcknowledges(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	day := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		h.src.rows = append(h.src.rows, staging.Row{SubjectID: "BTC", DataTime: day.Add(time.Duration(i) * time.Minute), Values: []any{float64(i)}})
	}
	task := staging.Task{SpaceID: "crypto", DatasetID: "ds_1m", Columns: []staging.Column{{Name: "close", Type: staging.TypeDouble}}}
	// 第一次：mgr 一直不可达，分区留在待上报里。
	for i := 0; i < completeRetries; i++ {
		h.mgr.completeErrs = append(h.mgr.completeErrs, errors.New("mgr down"))
	}
	h.mgr.assignments = []*archivepb.RunAssignment{assignment("r1", nil)}
	h.app.heartbeatOnce(ctx)
	h.app.pullOnce(ctx)
	h.idle()
	if pending, _ := h.store.PendingReports(ctx, "crypto", "ds_1m"); len(pending) != 1 {
		t.Fatalf("上报失败时分区应留在待上报里: %v", pending)
	}
	// 第二次：上报成功，随后清除标记。
	h.now = h.now.Add(time.Hour)
	h.mgr.assignments = []*archivepb.RunAssignment{assignment("r2", func(a *archivepb.RunAssignment) { a.StartedAt = "2026-10-17T02:04:43Z" })}
	h.app.heartbeatOnce(ctx)
	h.app.pullOnce(ctx)
	req := h.waitComplete()
	h.idle()
	if len(req.GetPartitions()) != 1 || req.GetPartitions()[0].GetPartitionKey() != "2026-10-17" {
		t.Fatalf("partitions = %v", req.GetPartitions())
	}
	if pending, _ := h.store.PendingReports(ctx, "crypto", "ds_1m"); len(pending) != 0 {
		t.Fatalf("mgr 确认登记后应清除待上报标记: %v", pending)
	}
	_ = task
}

func TestCompleteRetriesTransientFailuresAndStopsOnConflict(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.mgr.completeErrs = []error{errors.New("timeout"), errors.New("timeout"), nil}
	h.app.complete(ctx, &archivepb.CompleteArchiveRunReq{RunId: "r1"})
	if h.mgr.completeCalls != 3 || len(h.mgr.completes) != 1 {
		t.Fatalf("calls=%d completes=%d", h.mgr.completeCalls, len(h.mgr.completes))
	}
	if len(h.slept) != 2 || h.slept[0] != time.Second || h.slept[1] != 2*time.Second {
		t.Fatalf("退避 = %v", h.slept)
	}
	h2 := newHarness(t)
	h2.mgr.completeErrs = []error{errors.Join(manager.ErrConflict, errors.New("done"))}
	h2.app.complete(ctx, &archivepb.CompleteArchiveRunReq{RunId: "r1"})
	if h2.mgr.completeCalls != 1 {
		t.Fatalf("冲突不应重试: %d", h2.mgr.completeCalls)
	}
	h3 := newHarness(t)
	for i := 0; i < 10; i++ {
		h3.mgr.completeErrs = append(h3.mgr.completeErrs, errors.New("down"))
	}
	h3.app.complete(ctx, &archivepb.CompleteArchiveRunReq{RunId: "r1"})
	if h3.mgr.completeCalls != completeRetries {
		t.Fatalf("应在 %d 次后放弃: %d", completeRetries, h3.mgr.completeCalls)
	}
}

func TestMaintenanceDropsDeletedTasksAndWaitsForRuns(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	task := staging.Task{SpaceID: "crypto", DatasetID: "old", Columns: []staging.Column{{Name: "close", Type: staging.TypeDouble}}}
	if err := h.store.EnsureTable(ctx, task); err != nil {
		t.Fatal(err)
	}
	h.app.mu.Lock()
	h.app.deleted["crypto/old"] = taskRef{space: "crypto", dataset: "old", epoch: "1"}
	h.app.running["r1"] = &activeRun{assignment: assignment("r1", nil), cancel: func() {}}
	h.app.mu.Unlock()
	// 有运行一直不结束：等待到期后跳过，已删除的任务保留到下次。
	h.app.maintainOnce(ctx)
	if tables, _ := h.store.StagedTables(ctx); len(tables) != 1 {
		t.Fatalf("运行未结束不应清理: %v", tables)
	}
	if h.app.Snapshot().Maintaining {
		t.Fatal("维护结束后应恢复领取")
	}
	h.app.mu.Lock()
	delete(h.app.running, "r1")
	h.app.mu.Unlock()
	h.app.maintainOnce(ctx)
	if tables, _ := h.store.StagedTables(ctx); len(tables) != 0 || len(h.app.deleted) != 0 {
		t.Fatalf("tables=%v deleted=%v", tables, h.app.deleted)
	}
	total := time.Duration(0)
	for _, d := range h.slept {
		total += d
	}
	if total < maintenanceWait {
		t.Fatalf("应等满 %s: %s", maintenanceWait, total)
	}
}

func TestUntilMaintenanceIsTheNextConfiguredClockTime(t *testing.T) {
	h := newHarness(t)
	loc := time.FixedZone("test", 8*3600)
	h.now = time.Date(2026, 10, 17, 3, 0, 0, 0, loc)
	if got := h.app.untilMaintenance(); got != 1*time.Hour+17*time.Minute+43*time.Second {
		t.Fatalf("until = %s", got)
	}
	h.now = time.Date(2026, 10, 17, 5, 0, 0, 0, loc)
	if got := h.app.untilMaintenance(); got != 23*time.Hour+17*time.Minute+43*time.Second {
		t.Fatalf("until = %s", got)
	}
}

func TestRunStartsCleansLeftoversAndStopsGracefully(t *testing.T) {
	h := newHarness(t)
	stale := filepath.Join(h.root, "crypto", "ds", "2026", "x.csv.gz.partial")
	if err := mkfile(stale); err != nil {
		t.Fatal(err)
	}
	if err := h.app.Start(); err != nil {
		t.Fatal(err)
	}
	if fileExists(stale) {
		t.Fatal("启动时应清理残留的 .partial")
	}
	h.cfg.Worker.HeartbeatInterval, h.cfg.Worker.PullInterval = time.Second, time.Second
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	// 用真实睡眠的 App 运行主循环，验证取消后能退出。
	real := New(Options{Config: h.cfg, Manager: h.mgr, Runner: h.app.opts.Runner, Staging: h.store, Files: h.app.opts.Files, InstanceID: "i1", DiskUsage: h.app.opts.DiskUsage})
	go func() { done <- real.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.mgr.mu.Lock()
		n := len(h.mgr.heartbeats)
		h.mgr.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("主循环没有发出心跳")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("取消后主循环没有退出")
	}
	ids := real.runningIDs()
	sort.Strings(ids)
	if len(ids) != 0 {
		t.Fatalf("running = %v", ids)
	}
}
