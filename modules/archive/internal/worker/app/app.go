// Package app is the archive worker's main loop: heartbeats, pulling runs, executing them with bounded
// concurrency, reporting progress and results to the manager, and the daily maintenance.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/worker/config"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/export"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/manager"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/runner"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/staging"
	archivepb "github.com/mooyang-code/moox/modules/archive/proto/archivegen"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	progressEvery   = 30 * time.Second
	rpcTimeout      = 15 * time.Second
	completeRetries = 6
	// compactMinBytes 是暂存库文件小于它时不重建。
	compactMinBytes = 64 << 20
	// maintenanceWait 是每天维护最多等待进行中的运行结束多久。
	maintenanceWait = 30 * time.Minute
)

// DiskUsage 返回路径所在文件系统的总量与可用量。
type DiskUsage func(path string) (total, free uint64, err error)

// Options 是 App 的依赖。
type Options struct {
	Config     *config.Config
	Manager    manager.Client
	Runner     *runner.Runner
	Staging    *staging.Store
	Files      export.Files
	InstanceID string
	Version    string
	Now        func() time.Time
	Sleep      func(ctx context.Context, d time.Duration) error
	DiskUsage  DiskUsage
}

type taskRef struct{ space, dataset string }

type activeRun struct {
	assignment *archivepb.RunAssignment
	cancel     context.CancelFunc
	ctx        context.Context
	stop       atomic.Bool
}

// App 是 worker。
type App struct {
	opts Options

	mu              sync.Mutex
	active          bool
	leaseTTL        time.Duration
	lastHeartbeatOK time.Time
	lastError       string
	maintaining     bool
	diskLow         bool
	diskFree        uint64
	diskTotal       uint64
	running         map[string]*activeRun
	deleted         map[string]taskRef
	// pulling 是正在进行中的领取调用数：维护要等它们登记完运行之后再开始，否则重建暂存库时会漏掉在途的运行。
	pulling int
	// datasetSlots 让同一个数据集的运行在本地串行：mgr 取消一个运行后会立刻允许创建新运行，
	// 但旧运行要等下一次心跳才知道被取消，两个运行同时改写同一个暂存表和分区文件会丢行。
	datasetSlots  map[string]chan struct{}
	progressEvery time.Duration
	wg            sync.WaitGroup
}

// New 创建 App。
func New(opts Options) *App {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Sleep == nil {
		opts.Sleep = func(ctx context.Context, d time.Duration) error {
			timer := time.NewTimer(d)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		}
	}
	return &App{opts: opts, running: map[string]*activeRun{}, deleted: map[string]taskRef{}, datasetSlots: map[string]chan struct{}{}, leaseTTL: 45 * time.Second, progressEvery: progressEvery}
}

// Start 启动时的清理：残留的 .partial 文件和 DuckDB 临时目录。
func (a *App) Start() error {
	removed, err := a.opts.Files.CleanPartials()
	if err != nil {
		return fmt.Errorf("clean partial files: %w", err)
	}
	if removed > 0 {
		log.Warnf("archive worker removed %d leftover .partial files", removed)
	}
	return a.opts.Staging.CleanTemp()
}

// Run 运行主循环，直到 ctx 结束；退出时取消进行中的运行并等待它们收尾。
func (a *App) Run(ctx context.Context) error {
	if err := a.Start(); err != nil {
		return err
	}
	var loops sync.WaitGroup
	loop := func(interval func() time.Duration, fn func(context.Context)) {
		loops.Add(1)
		go func() {
			defer loops.Done()
			for {
				fn(ctx)
				if a.opts.Sleep(ctx, interval()) != nil {
					return
				}
			}
		}()
	}
	loop(func() time.Duration { return a.opts.Config.Worker.HeartbeatInterval }, a.heartbeatOnce)
	loop(func() time.Duration { return a.opts.Config.Worker.PullInterval }, a.pullOnce)
	loops.Add(1)
	go func() {
		defer loops.Done()
		a.maintenanceLoop(ctx)
	}()
	<-ctx.Done()
	loops.Wait()
	a.cancelAll()
	a.wg.Wait()
	return nil
}

func (a *App) cancelAll() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, r := range a.running {
		r.stop.Store(true)
		r.cancel()
	}
}

// ---- 心跳 ----

// busy 报告是否还有进行中的运行或在途的领取。
func (a *App) busy() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.running) > 0 || a.pulling > 0
}

func (a *App) runningIDs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	ids := make([]string, 0, len(a.running))
	for id := range a.running {
		ids = append(ids, id)
	}
	return ids
}

// HeartbeatOnce 发送一次心跳（主循环按配置的间隔调用；测试也直接调用）。
func (a *App) HeartbeatOnce(ctx context.Context) { a.heartbeatOnce(ctx) }

// PullOnce 领取并启动一次运行（同上）。
func (a *App) PullOnce(ctx context.Context) { a.pullOnce(ctx) }

func (a *App) heartbeatOnce(ctx context.Context) {
	cfg := a.opts.Config
	total, free, derr := a.opts.DiskUsage(cfg.Worker.RootDir)
	a.mu.Lock()
	if derr == nil {
		a.diskTotal, a.diskFree = total, free
		a.diskLow = int64(free) < int64(cfg.Worker.MinFreeBytes)
	}
	maintaining, diskLow := a.maintaining, a.diskLow
	a.mu.Unlock()
	pacingJSON, _ := json.Marshal(map[string]any{
		"request_seconds": cfg.Pacing.RequestSeconds, "hour_quiet": cfg.Pacing.HourQuiet,
		"max_bytes_per_second": int64(cfg.Pacing.MaxBytesPerSecond), "max_concurrent_runs": cfg.Pacing.MaxConcurrentRuns,
		"max_runs_per_data_node": cfg.Pacing.MaxRunsPerDataNode,
	})
	callCtx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	rsp, err := a.opts.Manager.Heartbeat(callCtx, &archivepb.ArchiveWorkerHeartbeatReq{
		WorkerId: cfg.Worker.WorkerID, InstanceId: a.opts.InstanceID, Version: a.opts.Version, RootDir: cfg.Worker.RootDir,
		DiskTotalBytes: total, DiskFreeBytes: free, StagingBytes: uint64(a.opts.Staging.FileBytes()),
		Maintaining: maintaining, DiskLow: diskLow, PacingJson: string(pacingJSON), RunningRunIds: a.runningIDs(),
	})
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		a.lastError = err.Error()
		if errors.Is(err, manager.ErrConflict) {
			// 同一个 worker_id 已有另一个存活的实例：本实例不领取运行。
			a.active = false
			log.Errorf("archive worker heartbeat conflict: %v", err)
		} else {
			log.Warnf("archive worker heartbeat failed: %v", err)
		}
		return
	}
	a.lastError = ""
	a.lastHeartbeatOK = a.opts.Now()
	a.active = rsp.GetActive()
	if ttl := time.Duration(rsp.GetLeaseTtlSeconds()) * time.Second; ttl > 0 {
		a.leaseTTL = ttl
	}
	for _, id := range rsp.GetCancelRunIds() {
		if r, ok := a.running[id]; ok {
			r.stop.Store(true)
			r.cancel()
		}
	}
	for _, ref := range rsp.GetDeletedTasks() {
		a.deleted[ref.GetSpaceId()+"/"+ref.GetDatasetId()] = taskRef{space: ref.GetSpaceId(), dataset: ref.GetDatasetId()}
	}
}

// leaseValid 报告本实例的租约在本地看来是否仍有效：最近一次成功的心跳在租约有效期内。
func (a *App) leaseValid() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.active && !a.lastHeartbeatOK.IsZero() && a.opts.Now().Sub(a.lastHeartbeatOK) < a.leaseTTL
}

// ---- 领取与执行 ----

func (a *App) pullOnce(ctx context.Context) {
	cfg := a.opts.Config
	a.mu.Lock()
	free := cfg.Pacing.MaxConcurrentRuns - len(a.running) - a.pulling
	blocked := a.maintaining || a.diskLow
	busy := map[string]uint32{}
	for _, r := range a.running {
		busy[r.assignment.GetDataNodeId()]++
	}
	if free > 0 && !blocked && a.active && !a.lastHeartbeatOK.IsZero() && a.opts.Now().Sub(a.lastHeartbeatOK) < a.leaseTTL {
		// 与维护的「开始」在同一把锁里决定：登记了 pulling 之后，维护会等这次领取登记完运行才继续。
		a.pulling++
	} else {
		a.mu.Unlock()
		return
	}
	a.mu.Unlock()
	registered := false
	defer func() {
		if !registered {
			a.mu.Lock()
			a.pulling--
			a.mu.Unlock()
		}
	}()
	var loads []*archivepb.DataNodeLoad
	for node, n := range busy {
		loads = append(loads, &archivepb.DataNodeLoad{DataNodeId: node, Running: n})
	}
	callCtx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	rsp, err := a.opts.Manager.Pull(callCtx, &archivepb.PullArchiveRunReq{
		WorkerId: cfg.Worker.WorkerID, InstanceId: a.opts.InstanceID, FreeSlots: uint32(free),
		MaxRunsPerDataNode: uint32(cfg.Pacing.MaxRunsPerDataNode), BusyDataNodes: loads,
	})
	if err != nil {
		log.Warnf("archive worker pull failed: %v", err)
		return
	}
	// 先把全部运行登记进 running，再释放 pulling，维护才不会在两者之间看到「空闲」。
	a.mu.Lock()
	runs := make([]*activeRun, 0, len(rsp.GetRuns()))
	for _, assignment := range rsp.GetRuns() {
		runCtx, cancel := context.WithCancel(ctx)
		run := &activeRun{assignment: assignment, cancel: cancel, ctx: runCtx}
		a.running[assignment.GetRunId()] = run
		// 数据集重新创建了任务：之前记下的「已删除」通知作废，不能再清掉它的暂存数据。
		delete(a.deleted, assignment.GetSpaceId()+"/"+assignment.GetDatasetId())
		runs = append(runs, run)
	}
	a.pulling--
	registered = true
	a.mu.Unlock()
	for _, run := range runs {
		a.launch(run)
	}
}

func (a *App) launch(run *activeRun) {
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer run.cancel()
		a.execute(run.ctx, run)
		a.mu.Lock()
		delete(a.running, run.assignment.GetRunId())
		a.mu.Unlock()
	}()
}

// acquireDataset 等到同一个数据集没有其他运行在本地执行；等待期间照常续租（上报零进度），否则排队的运行会因租约过期被判失联。
func (a *App) acquireDataset(ctx context.Context, run *activeRun, progress func()) (release func(), err error) {
	key := run.assignment.GetSpaceId() + "/" + run.assignment.GetDatasetId()
	a.mu.Lock()
	slot, ok := a.datasetSlots[key]
	if !ok {
		slot = make(chan struct{}, 1)
		a.datasetSlots[key] = slot
	}
	a.mu.Unlock()
	for {
		select {
		case slot <- struct{}{}:
			return func() { <-slot }, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
			if run.stop.Load() {
				return nil, context.Canceled
			}
			progress()
		}
	}
}

// execute 执行一次运行并上报结果。
func (a *App) execute(ctx context.Context, run *activeRun) {
	assignment := run.assignment
	var latest atomic.Pointer[runner.Progress]
	latest.Store(&runner.Progress{})
	var lastSent atomic.Int64
	send := func() {
		p := latest.Load()
		callCtx, cancel := context.WithTimeout(ctx, rpcTimeout)
		rsp, err := a.opts.Manager.Progress(callCtx, &archivepb.ReportArchiveRunProgressReq{
			WorkerId: a.opts.Config.Worker.WorkerID, RunId: assignment.GetRunId(), LeaseToken: assignment.GetLeaseToken(),
			SubjectsDone: p.SubjectsDone, RequestCount: p.RequestCount, BytesRead: p.BytesRead, RowsRead: p.RowsRead,
		})
		cancel()
		lastSent.Store(a.opts.Now().UnixNano())
		switch {
		case err != nil:
			log.Warnf("archive worker progress report failed run=%s: %v", assignment.GetRunId(), err)
		case rsp.GetCanceled():
			run.stop.Store(true)
			run.cancel()
		}
	}
	// 续租与进度由独立的定时器负责，不依赖「读完一页」：导出、定稿和整点避让等待期间也要续租。
	tickCtx, stopTicker := context.WithCancel(ctx)
	var ticker sync.WaitGroup
	ticker.Add(1)
	go func() {
		defer ticker.Done()
		timer := time.NewTicker(a.progressEvery)
		defer timer.Stop()
		for {
			select {
			case <-tickCtx.Done():
				return
			case <-timer.C:
				send()
			}
		}
	}()
	stopped := false
	stopTick := func() {
		if !stopped {
			stopped = true
			stopTicker()
			ticker.Wait()
		}
	}
	defer stopTick()

	req := &archivepb.CompleteArchiveRunReq{WorkerId: a.opts.Config.Worker.WorkerID, RunId: assignment.GetRunId(), LeaseToken: assignment.GetLeaseToken()}
	var reported []string
	release, err := a.acquireDataset(ctx, run, send)
	if err != nil {
		req.Status, req.Message = "canceled", "运行在等待同一数据集的上一个运行时被取消"
	} else {
		report := func(p runner.Progress) bool {
			cp := p
			latest.Store(&cp)
			return run.stop.Load()
		}
		outcome, execErr := a.opts.Runner.Execute(ctx, assignment, report)
		release()
		if execErr != nil || outcome == nil {
			req.Status, req.Message = "failed", fmt.Sprintf("%v", execErr)
		} else {
			req.Status, req.Message = outcome.Status, outcome.Message
			req.SubjectsDone, req.RequestCount, req.BytesRead, req.RowsRead = outcome.Progress.SubjectsDone, outcome.Progress.RequestCount, outcome.Progress.BytesRead, outcome.Progress.RowsRead
			req.RowsAdded, req.RowsChanged = outcome.RowsAdded, outcome.RowsChanged
			req.PartitionsExported, req.PartitionsFinalized = outcome.Exported, outcome.Finalized
			req.Partitions, req.Unrecoverable = outcome.Partitions, outcome.Unrecoverable
			reported = outcome.ReportedKeys
			if !outcome.FinalUntil.IsZero() {
				req.FinalUntil = outcome.FinalUntil.UTC().Format(time.RFC3339)
			}
		}
	}
	stopTick()
	if a.complete(ctx, req) && len(reported) > 0 {
		// mgr 确认登记之后才清除待上报标记；上报失败时分区留在待上报里，下一次运行会再带上。
		if err := a.opts.Staging.MarkReported(context.WithoutCancel(ctx), assignment.GetSpaceId(), assignment.GetDatasetId(), reported); err != nil {
			log.Warnf("archive worker mark reported run=%s: %v", assignment.GetRunId(), err)
		}
	}
}

// complete 上报结果：暂时失败时退避重试；mgr 返回冲突（运行已结束或令牌失效）时放弃。返回 true 表示 mgr 已确认。
// 进程退出（ctx 取消）时用一个独立的短超时再试一次，让被取消的运行也能登记已导出的分区。
func (a *App) complete(ctx context.Context, req *archivepb.CompleteArchiveRunReq) bool {
	for attempt := 0; attempt < completeRetries; attempt++ {
		callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rpcTimeout)
		_, err := a.opts.Manager.Complete(callCtx, req)
		cancel()
		if err == nil {
			return true
		}
		if errors.Is(err, manager.ErrConflict) {
			log.Warnf("archive worker complete rejected run=%s: %v", req.GetRunId(), err)
			return false
		}
		log.Warnf("archive worker complete failed run=%s attempt=%d: %v", req.GetRunId(), attempt+1, err)
		if ctx.Err() != nil && attempt >= 1 {
			return false
		}
		if attempt+1 < completeRetries {
			backoff := time.Duration(1<<attempt) * time.Second
			if a.opts.Sleep(context.WithoutCancel(ctx), backoff) != nil {
				return false
			}
		}
	}
	log.Errorf("archive worker gave up reporting run=%s; the manager will mark it failed when its lease expires and the partitions stay pending for the next run", req.GetRunId())
	return false
}

// ---- 维护 ----

func (a *App) maintenanceLoop(ctx context.Context) {
	for {
		if a.opts.Sleep(ctx, a.untilMaintenance()) != nil {
			return
		}
		a.maintainOnce(ctx)
	}
}

// untilMaintenance 返回距离下一个维护时刻（本机时区）的时长。
func (a *App) untilMaintenance() time.Duration {
	at, err := config.ParseClock(a.opts.Config.Maintenance.At)
	if err != nil {
		at = 4*time.Hour + 17*time.Minute + 43*time.Second
	}
	now := a.opts.Now()
	next := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Add(at)
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next.Sub(now)
}

// maintainOnce 执行一次维护：先停止领取并等进行中的运行结束，再清理已删除任务、CHECKPOINT、必要时重建库文件、清理临时目录。
func (a *App) maintainOnce(ctx context.Context) {
	a.mu.Lock()
	a.maintaining = true
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.maintaining = false
		a.mu.Unlock()
	}()
	deadline := a.opts.Now().Add(maintenanceWait)
	for a.busy() {
		if a.opts.Now().After(deadline) || ctx.Err() != nil {
			log.Warnf("archive worker maintenance skipped: runs are still active")
			return
		}
		if a.opts.Sleep(ctx, 10*time.Second) != nil {
			return
		}
	}
	a.mu.Lock()
	pending := make([]taskRef, 0, len(a.deleted))
	for _, ref := range a.deleted {
		pending = append(pending, ref)
	}
	a.mu.Unlock()
	for _, ref := range pending {
		if err := a.opts.Staging.DropTask(ctx, ref.space, ref.dataset); err != nil {
			log.Warnf("archive worker drop deleted task %s/%s: %v", ref.space, ref.dataset, err)
			continue
		}
		a.mu.Lock()
		delete(a.deleted, ref.space+"/"+ref.dataset)
		a.mu.Unlock()
	}
	if err := a.opts.Staging.Checkpoint(ctx); err != nil {
		log.Warnf("archive worker checkpoint: %v", err)
	}
	if compacted, err := a.opts.Staging.Compact(ctx, a.opts.Config.Staging.RebuildRatio, compactMinBytes); err != nil {
		log.Errorf("archive worker compact staging database: %v", err)
	} else if compacted {
		log.Infof("archive worker rebuilt the staging database")
	}
	if err := a.opts.Staging.CleanTemp(); err != nil {
		log.Warnf("archive worker clean temp: %v", err)
	}
}

// Snapshot 是健康检查用的状态。
type Snapshot struct {
	Ready       bool
	Active      bool
	Running     int
	Maintaining bool
	DiskLow     bool
	DiskFree    uint64
	LastError   string
}

// Snapshot 返回当前状态：最近一次心跳成功即就绪。
func (a *App) Snapshot() Snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return Snapshot{
		Ready:  !a.lastHeartbeatOK.IsZero() && a.opts.Now().Sub(a.lastHeartbeatOK) < 3*a.opts.Config.Worker.HeartbeatInterval,
		Active: a.active, Running: len(a.running), Maintaining: a.maintaining, DiskLow: a.diskLow, DiskFree: a.diskFree, LastError: a.lastError,
	}
}
