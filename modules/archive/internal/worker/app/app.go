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

	"github.com/mooyang-code/moox/modules/archive/internal/objectstore"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/config"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/export"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/manager"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/pacing"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/runner"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/staging"
	archivepb "github.com/mooyang-code/moox/modules/archive/proto/archivegen"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	progressEvery = 30 * time.Second
	// importRetention 是失败的导入在 <root>/.import 下保留的时长。
	importRetention = 7 * 24 * time.Hour
	rpcTimeout      = 15 * time.Second
	completeRetries = 6
	// maintenanceWait 是每天维护最多等待进行中的运行结束多久。
	maintenanceWait = 30 * time.Minute
	// probeEvery 是对象存储可用性探测的间隔。
	probeEvery = time.Minute
	// probeKey 是探测用的固定且不存在的键（客户端会加上 prefix）：返回 404 说明对象存储可用。
	probeKey = "_probe/absent"
)

// DiskUsage 返回路径所在文件系统的总量、已用与可用量（可用不含 root 保留空间）。
type DiskUsage func(path string) (total, used, free uint64, err error)

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
	// Remote 是对象存储客户端；nil 表示没有配置。心跳带上它的摘要，每天维护时抽查远端副本。
	Remote *objectstore.Client
	// Pacer 是与 Runner 共用的节奏控制：心跳回包里的避让规则替换它的生效规则；nil 时不接收（测试）。
	Pacer *pacing.Pacer
}

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
	// diskInsufficient：可用空间低于 worker.min_free_bytes，停止领取新运行。
	diskInsufficient bool
	// diskTotal、diskUsed、diskFree 是最近一次成功读到的磁盘用量；读取失败时心跳照报它们，与 diskInsufficient 的依据一致。
	diskTotal uint64
	diskUsed  uint64
	diskFree  uint64
	running   map[string]*activeRun
	// pulling 是正在进行中的领取调用数：维护要等它们登记完运行之后再开始，否则对账时会漏掉在途的运行。
	pulling int
	// datasetSlots 让同一个数据集的运行在本地串行：mgr 取消一个运行后会立刻允许创建新运行，
	// 但旧运行要等下一次心跳才知道被取消，两个运行同时改写同一个暂存表和分区文件会丢行。
	datasetSlots  map[string]chan struct{}
	progressEvery time.Duration
	// probeError 是最近一次对象存储探测失败的原因，unavailableSince 是连续不可用的起点；可用时都为空。
	probeError       string
	unavailableSince time.Time
	wg               sync.WaitGroup
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
	return &App{opts: opts, running: map[string]*activeRun{}, datasetSlots: map[string]chan struct{}{}, leaseTTL: 45 * time.Second, progressEvery: progressEvery}
}

// Start 启动时的清理：残留的 .partial 文件。
func (a *App) Start() error {
	removed, err := a.opts.Files.CleanPartials()
	if err != nil {
		return fmt.Errorf("clean partial files: %w", err)
	}
	if removed > 0 {
		log.Warnf("archive worker removed %d leftover .partial files", removed)
	}
	return nil
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
	loop(func() time.Duration { return a.opts.Config.Worker.HeartbeatInterval }, func(ctx context.Context) { a.heartbeatOnce(ctx) })
	loop(func() time.Duration { return a.opts.Config.Worker.PullInterval }, a.pullOnce)
	if a.opts.Remote != nil {
		// 单独一个循环：探测可能因超时与重试卡几十秒，不能拖住心跳。启动后立即做第一次探测。
		loop(func() time.Duration { return probeEvery }, a.probeOnce)
	}
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

// MaintainOnce 执行一次维护（同上）。
func (a *App) MaintainOnce(ctx context.Context) { a.maintainOnce(ctx) }

// PullOnce 领取并启动一次运行（同上）。
func (a *App) PullOnce(ctx context.Context) { a.pullOnce(ctx) }

// ProbeOnce 探测一次对象存储（同上）。
func (a *App) ProbeOnce(ctx context.Context) { a.probeOnce(ctx) }

// probeOnce 对固定且不存在的键做 HEAD：404 算可用；连接失败、403、5xx 等都算不可用。结果随心跳上报。
func (a *App) probeOnce(ctx context.Context) {
	_, err := a.opts.Remote.Head(ctx, probeKey)
	if ctx.Err() != nil {
		return // 退出时被取消，不算对象存储的问题
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	// 正常是 404；有人放了这个键时 HEAD 成功，同样说明能连通且有权限。
	if err == nil || errors.Is(err, objectstore.ErrNotFound) {
		a.probeError, a.unavailableSince = "", time.Time{}
		return
	}
	if a.probeError == "" {
		log.Warnf("archive worker object store unavailable: %v", err)
	}
	a.probeError = objectstore.ErrorText(err)
	if a.unavailableSince.IsZero() {
		a.unavailableSince = a.opts.Now().UTC()
	}
}

// heartbeatOnce 发送一次心跳。本实例是活动 worker 时 mgr 下发当前全部任务，返回它们（键 "space/dataset"，值 task_id）；否则返回 nil。
func (a *App) heartbeatOnce(ctx context.Context) map[string]string {
	cfg := a.opts.Config
	total, used, free, derr := a.opts.DiskUsage(cfg.Worker.RootDir)
	a.mu.Lock()
	if derr == nil {
		a.diskTotal, a.diskUsed, a.diskFree = total, used, free
		a.diskInsufficient = int64(free) < int64(cfg.Worker.MinFreeBytes)
	} else {
		log.Warnf("archive worker read disk usage of %s: %v", cfg.Worker.RootDir, derr)
	}
	total, used, free = a.diskTotal, a.diskUsed, a.diskFree
	maintaining := a.maintaining
	a.mu.Unlock()
	pacingJSON := a.pacingJSON()
	callCtx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	rsp, err := a.opts.Manager.Heartbeat(callCtx, &archivepb.ArchiveWorkerHeartbeatReq{
		WorkerId: cfg.Worker.WorkerID, InstanceId: a.opts.InstanceID, Version: a.opts.Version, RootDir: cfg.Worker.RootDir,
		DiskTotalBytes: total, DiskFreeBytes: free, DiskUsedBytes: used, MinFreeBytes: uint64(cfg.Worker.MinFreeBytes),
		StagingBytes: uint64(a.opts.Staging.FileBytes()), Maintaining: maintaining, PacingJson: string(pacingJSON), RunningRunIds: a.runningIDs(),
		ObjectStore: a.objectStoreInfo(),
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
		return nil
	}
	a.lastError = ""
	a.lastHeartbeatOK = a.opts.Now()
	a.applyAvoidance(rsp.GetAvoidance())
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
	if !rsp.GetActive() {
		return nil
	}
	tasks := make(map[string]string, len(rsp.GetTasks()))
	for _, ref := range rsp.GetTasks() {
		tasks[ref.GetSpaceId()+"/"+ref.GetDatasetId()] = ref.GetTaskId()
	}
	return tasks
}

// pacingJSON 是心跳上报的生效节奏：避让规则取 pacer 当前值（mgr 下发或 app.yaml），其余取配置。
func (a *App) pacingJSON() string {
	cfg := a.opts.Config
	rules, minuteWindow := cfg.PacingConfig().ConfigRules(), cfg.Pacing.MinuteWindow
	if a.opts.Pacer != nil {
		rules, minuteWindow = a.opts.Pacer.Rules(), a.opts.Pacer.MinuteWindow()
	}
	var hourQuiet []string
	if rules.HourQuiet != nil {
		hourQuiet = rules.HourQuiet[:]
	}
	raw, _ := json.Marshal(map[string]any{
		"source": rules.Source, "computed_at": rules.ComputedAt, "minute_window": minuteWindow,
		"every_minutes": rules.EveryMinutes, "before_seconds": rules.BeforeSeconds, "after_seconds": rules.AfterSeconds, "hour_quiet": hourQuiet,
		"max_bytes_per_second": int64(cfg.Pacing.MaxBytesPerSecond), "max_concurrent_runs": cfg.Pacing.MaxConcurrentRuns,
		"max_runs_per_data_node": cfg.Pacing.MaxRunsPerDataNode,
	})
	return string(raw)
}

// applyAvoidance 用心跳回包里的避让规则替换 pacer 的生效规则；没有下发或不合法时保留原规则。
func (a *App) applyAvoidance(rules *archivepb.AvoidanceRules) {
	if rules == nil || a.opts.Pacer == nil {
		return
	}
	next := pacing.Rules{
		EveryMinutes: int(rules.GetEveryMinutes()), BeforeSeconds: int(rules.GetBeforeSeconds()), AfterSeconds: int(rules.GetAfterSeconds()),
		Source: "mgr", ComputedAt: rules.GetComputedAt(),
	}
	if rules.GetHourQuietFrom() != "" || rules.GetHourQuietUntil() != "" {
		next.HourQuiet = &[2]string{rules.GetHourQuietFrom(), rules.GetHourQuietUntil()}
	}
	prev := a.opts.Pacer.Rules()
	if err := a.opts.Pacer.SetRules(next); err != nil {
		log.Warnf("archive worker ignored avoidance rules from mgr: %v", err)
		return
	}
	if prev.Source != next.Source || prev.EveryMinutes != next.EveryMinutes || prev.BeforeSeconds != next.BeforeSeconds ||
		prev.AfterSeconds != next.AfterSeconds || fmt.Sprint(prev.HourQuiet) != fmt.Sprint(next.HourQuiet) {
		log.Infof("archive worker avoidance updated: every=%dm before=%ds after=%ds hour_quiet=%v basis=%s",
			next.EveryMinutes, next.BeforeSeconds, next.AfterSeconds, next.HourQuiet, rules.GetBasis())
	}
}

// ---- 领取与执行 ----

func (a *App) pullOnce(ctx context.Context) {
	cfg := a.opts.Config
	a.mu.Lock()
	free := cfg.Pacing.MaxConcurrentRuns - len(a.running) - a.pulling
	blocked := a.maintaining || a.diskInsufficient
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

// acquireDataset 等到同一个数据集没有其他运行在本地执行；等待期间由运行的续租定时器照常续租。
func (a *App) acquireDataset(ctx context.Context, run *activeRun) (release func(), err error) {
	key := run.assignment.GetSpaceId() + "/" + run.assignment.GetDatasetId()
	a.mu.Lock()
	slot, ok := a.datasetSlots[key]
	if !ok {
		slot = make(chan struct{}, 1)
		a.datasetSlots[key] = slot
	}
	a.mu.Unlock()
	select {
	case slot <- struct{}{}:
		return func() { <-slot }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// execute 执行一次运行并上报结果。
func (a *App) execute(ctx context.Context, run *activeRun) {
	assignment := run.assignment
	var latest atomic.Pointer[runner.Progress]
	latest.Store(&runner.Progress{})
	send := func() {
		p := latest.Load()
		callCtx, cancel := context.WithTimeout(ctx, rpcTimeout)
		rsp, err := a.opts.Manager.Progress(callCtx, &archivepb.ReportArchiveRunProgressReq{
			WorkerId: a.opts.Config.Worker.WorkerID, RunId: assignment.GetRunId(), LeaseToken: assignment.GetLeaseToken(),
			SubjectsDone: p.SubjectsDone, RequestCount: p.RequestCount, BytesRead: p.BytesRead, RowsRead: p.RowsRead,
		})
		cancel()
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
	// 同数据集的串行保护延续到完成上报和确认清理之后：否则取消的旧运行的迟到上报可能覆盖新运行刚登记的分区。
	release, err := a.acquireDataset(ctx, run)
	if err == nil {
		defer release()
	}
	if err != nil {
		req.Status, req.Message = "canceled", "运行在等待同一数据集的上一个运行时被取消"
	} else {
		report := func(p runner.Progress) bool {
			cp := p
			latest.Store(&cp)
			return run.stop.Load()
		}
		outcome, execErr := a.opts.Runner.Execute(ctx, assignment, report)
		if execErr != nil || outcome == nil {
			req.Status, req.Message = "failed", fmt.Sprintf("%v", execErr)
		} else {
			req.Status, req.Message = outcome.Status, outcome.Message
			req.SubjectsDone, req.RequestCount, req.BytesRead, req.RowsRead = outcome.Progress.SubjectsDone, outcome.Progress.RequestCount, outcome.Progress.BytesRead, outcome.Progress.RowsRead
			req.RowsAdded, req.RowsChanged = outcome.RowsAdded, outcome.RowsChanged
			req.PartitionsExported, req.PartitionsFinalized, req.PartitionsUploaded = outcome.Exported, outcome.Finalized, outcome.Uploaded
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

// maintainOnce 执行一次维护：先停止领取并等进行中的运行结束，再发一次心跳取当前全部任务做对账
// （拿不到任务列表就跳过对账），最后回收暂存库空闲页并截断 WAL。
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
	// 用这次心跳自己的结果：缓存的旧列表可能早已过时。
	if keep := a.heartbeatOnce(ctx); keep == nil {
		log.Warnf("archive worker maintenance: 没有拿到当前任务列表，跳过对账")
	} else if dropped, err := a.opts.Staging.Reconcile(ctx, keep); err != nil {
		log.Warnf("archive worker reconcile staging: %v", err)
	} else if len(dropped) > 0 {
		log.Infof("archive worker dropped staging state of deleted tasks: %v", dropped)
	}
	// 失败的导入保留导入目录供核对，超过 7 天的清掉。
	if removed, err := a.opts.Files.CleanImports(a.opts.Now(), importRetention); err != nil {
		log.Warnf("archive worker clean import files: %v", err)
	} else if removed > 0 {
		log.Infof("archive worker removed %d import directories older than 7 days", removed)
	}
	a.checkRemote(ctx)
	if err := a.opts.Staging.Maintain(ctx); err != nil {
		log.Warnf("archive worker staging maintenance: %v", err)
	}
}

// objectStoreInfo 返回心跳里的对象存储摘要；没有配置时为空。
func (a *App) objectStoreInfo() *archivepb.ObjectStoreInfo {
	if a.opts.Remote == nil {
		return nil
	}
	info := a.opts.Remote.Info()
	a.mu.Lock()
	defer a.mu.Unlock()
	return &archivepb.ObjectStoreInfo{
		Endpoint: info.Endpoint, Bucket: info.Bucket, Prefix: info.Prefix, StorageClass: info.StorageClass, UrlStyle: info.URLStyle,
		ProbeError: a.probeError, UnavailableSince: fmtTime(a.unavailableSince),
	}
}

// fmtTime 把时间格式化为 RFC3339（UTC），零值为空。
func fmtTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// Snapshot 是健康检查用的状态。
type Snapshot struct {
	Ready       bool
	Active      bool
	Running     int
	Maintaining bool
	// DiskInsufficient：可用空间低于下限，已停止领取新运行。
	DiskInsufficient bool
	DiskFree         uint64
	LastError        string
}

// Snapshot 返回当前状态：最近一次心跳成功即就绪。
func (a *App) Snapshot() Snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return Snapshot{
		Ready:  !a.lastHeartbeatOK.IsZero() && a.opts.Now().Sub(a.lastHeartbeatOK) < 3*a.opts.Config.Worker.HeartbeatInterval,
		Active: a.active, Running: len(a.running), Maintaining: a.maintaining, DiskInsufficient: a.diskInsufficient, DiskFree: a.diskFree, LastError: a.lastError,
	}
}
