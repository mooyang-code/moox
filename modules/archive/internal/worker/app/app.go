// Package app is the archive worker's main loop: heartbeats, local follow loop, pulling manual/range/import runs,
// executing them with bounded concurrency, reporting progress and results to the manager, and the daily maintenance.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/objectstore"
	"github.com/mooyang-code/moox/modules/archive/internal/schedule"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/config"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/dataapi"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/export"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/manager"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/runner"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/staging"
	archivepb "github.com/mooyang-code/moox/modules/archive/proto/archivegen"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	progressEvery   = 30 * time.Second
	rpcTimeout      = 15 * time.Second
	completeRetries = 6
	// maintenanceWait 是每天维护最多等待进行中的运行与跟随结束多久。
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
	Config    *config.Config
	Manager   manager.Client
	Runner    *runner.Runner
	Staging   *staging.Store
	Files     export.Files
	Version   string
	Now       func() time.Time
	Sleep     func(ctx context.Context, d time.Duration) error
	DiskUsage DiskUsage
	// Remote 是对象存储客户端；nil 表示没有配置。心跳带上它的摘要，每天维护时抽查远端副本。
	Remote *objectstore.Client
}

type activeRun struct {
	assignment *archivepb.RunAssignment
	cancel     context.CancelFunc
	ctx        context.Context
	stop       atomic.Bool
	// batches、finished 只给 import 运行用：数据端口把批次放进 batches，由运行协程写暂存区；finished 关闭表示批次已写完。
	batches    chan runner.ImportBatch
	finished   chan struct{}
	finishOnce sync.Once
}

type activeFollow struct {
	task   *archivepb.FollowTask
	cancel context.CancelFunc
	ctx    context.Context
	stop   atomic.Bool
}

var _ dataapi.ImportSink = (*App)(nil)

// App 是 worker。
type App struct {
	opts Options

	mu              sync.Mutex
	lastHeartbeatOK time.Time
	lastError       string
	maintaining     bool
	// diskInsufficient：可用空间低于 worker.min_free_bytes，停止跟随与领取新运行。
	diskInsufficient bool
	// diskTotal、diskUsed、diskFree 是最近一次成功读到的磁盘用量；读取失败时心跳照报它们，与 diskInsufficient 的依据一致。
	diskTotal uint64
	diskUsed  uint64
	diskFree  uint64
	running   map[string]*activeRun
	// following 记录本地正在执行的跟随（键为 "space/dataset"），计入总并发槽与读 Storage 并发占用。
	following map[string]*activeFollow
	// lastFollowStart 记录每个任务最近一次本地跟随的开始时刻，用于在多任务争抢读 Storage 名额时按最久未跟随优先轮转。
	lastFollowStart map[string]time.Time
	// pulling、followListing 是正在进行中的领取与拉取跟随清单调用数：维护要等它们登记完之后再开始，否则对账时会漏掉在途任务。
	pulling       int
	followListing int
	// datasetSlots 让同一个数据集的执行（本地跟随与手动、补拉、导入运行）在本地串行：
	// 领到的手动运行等当轮跟随让出数据集，等待期间续租定时器照常续租，不会空耗租约。
	datasetSlots map[string]chan struct{}
	// slotFreed 在任意运行或跟随结束、或进入维护/磁盘不足时通知等待并发槽的 followOnce。
	slotFreed     chan struct{}
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
	return &App{
		opts:            opts,
		running:         map[string]*activeRun{},
		following:       map[string]*activeFollow{},
		lastFollowStart: map[string]time.Time{},
		datasetSlots:    map[string]chan struct{}{},
		slotFreed:       make(chan struct{}, 1),
		progressEvery:   progressEvery,
	}
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

// Run 运行主循环，直到 ctx 结束；退出时取消进行中的运行与跟随并等待它们收尾。
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
	loop(func() time.Duration { return a.opts.Config.Worker.HeartbeatInterval }, func(ctx context.Context) { a.heartbeatOnce(ctx, false) })
	loop(func() time.Duration { return a.opts.Config.Worker.PullInterval }, a.pullOnce)
	if a.opts.Remote != nil {
		// 单独一个循环：探测可能因超时与重试卡几十秒，不能拖住心跳。启动后立即做第一次探测。
		loop(func() time.Duration { return probeEvery }, a.probeOnce)
	}
	loops.Add(1)
	go func() {
		defer loops.Done()
		a.followLoop(ctx)
	}()
	loops.Add(1)
	go func() {
		defer loops.Done()
		a.maintenanceLoop(ctx)
	}()
	<-ctx.Done()
	a.cancelAll()
	loops.Wait()
	a.wg.Wait()
	return nil
}

func (a *App) cancelAll() {
	a.mu.Lock()
	for _, r := range a.running {
		r.stop.Store(true)
		r.cancel()
	}
	for _, f := range a.following {
		f.stop.Store(true)
		f.cancel()
	}
	a.mu.Unlock()
	a.notifySlot()
}

func (a *App) notifySlot() {
	select {
	case a.slotFreed <- struct{}{}:
	default:
	}
}

func (a *App) waitSlot(ctx context.Context) error {
	select {
	case <-a.slotFreed:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ---- 心跳 ----

// busy 报告是否还有进行中的运行、本地跟随或在途的领取/拉取。
func (a *App) busy() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.running) > 0 || len(a.following) > 0 || a.pulling > 0 || a.followListing > 0
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

// busyStorageRunsLocked 返回当前占用读 Storage 并发名额的执行数（本地跟随 + 非 import 运行）；须在持有 a.mu 时调用。
func (a *App) busyStorageRunsLocked() int {
	busy := len(a.following)
	for _, r := range a.running {
		// 导入不读 Storage 时序数据（只取一次列信息），不计入读 Storage 的并发占用（与 mgr 领取时的口径一致）。
		if r.assignment.GetKind() != "import" {
			busy++
		}
	}
	return busy
}

// hasActiveRunLocked 报告指定数据集是否已有正在执行的手动、补拉或导入运行；须在持有 a.mu 时调用。
func (a *App) hasActiveRunLocked(key string) bool {
	for _, r := range a.running {
		if r.assignment.GetSpaceId()+"/"+r.assignment.GetDatasetId() == key {
			return true
		}
	}
	return false
}

// HeartbeatOnce 发送一次常规心跳（主循环按配置的间隔调用；测试也直接调用）。
func (a *App) HeartbeatOnce(ctx context.Context) { a.heartbeatOnce(ctx, false) }

// MaintainOnce 执行一次维护（同上）。
func (a *App) MaintainOnce(ctx context.Context) { a.maintainOnce(ctx) }

// PullOnce 领取并启动一次手动/区间/导入运行（同上）。
func (a *App) PullOnce(ctx context.Context) { a.pullOnce(ctx) }

// FollowOnce 拉取启用任务清单并执行一轮本地跟随，等待本轮全部任务完成后返回（主循环每分钟调用；测试也直接调用）。
func (a *App) FollowOnce(ctx context.Context) { a.followOnce(ctx) }

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

// heartbeatOnce 发送一次心跳。includeTasks 为真时（每日维护对账）向 mgr 请求当前全部任务并返回（键 "space/dataset"，值 task_id）；心跳失败返回 nil。
func (a *App) heartbeatOnce(ctx context.Context, includeTasks bool) map[string]string {
	cfg := a.opts.Config
	total, used, free, derr := a.opts.DiskUsage(cfg.Worker.RootDir)
	a.mu.Lock()
	prevInsufficient := a.diskInsufficient
	if derr == nil {
		a.diskTotal, a.diskUsed, a.diskFree = total, used, free
		a.diskInsufficient = int64(free) < int64(cfg.Worker.MinFreeBytes)
	} else {
		log.Warnf("archive worker read disk usage of %s: %v", cfg.Worker.RootDir, derr)
	}
	total, used, free = a.diskTotal, a.diskUsed, a.diskFree
	maintaining := a.maintaining
	nowInsufficient := a.diskInsufficient
	a.mu.Unlock()
	if nowInsufficient && !prevInsufficient {
		a.notifySlot()
	}
	pacingJSON := a.pacingJSON()
	callCtx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	rsp, err := a.opts.Manager.Heartbeat(callCtx, &archivepb.ArchiveWorkerHeartbeatReq{
		WorkerId: cfg.Worker.WorkerID, Version: a.opts.Version, RootDir: cfg.Worker.RootDir,
		DiskTotalBytes: total, DiskFreeBytes: free, DiskUsedBytes: used, MinFreeBytes: uint64(cfg.Worker.MinFreeBytes),
		StagingBytes: uint64(a.opts.Staging.FileBytes()), Maintaining: maintaining, PacingJson: string(pacingJSON), RunningRunIds: a.runningIDs(),
		ObjectStore: a.objectStoreInfo(), IncludeTasks: includeTasks,
	})
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		a.lastError = err.Error()
		log.Warnf("archive worker heartbeat failed: %v", err)
		return nil
	}
	a.lastError = ""
	a.lastHeartbeatOK = a.opts.Now()
	for _, id := range rsp.GetCancelRunIds() {
		if r, ok := a.running[id]; ok {
			r.stop.Store(true)
			r.cancel()
		}
	}
	tasks := make(map[string]string, len(rsp.GetTasks()))
	for _, ref := range rsp.GetTasks() {
		tasks[ref.GetSpaceId()+"/"+ref.GetDatasetId()] = ref.GetTaskId()
	}
	return tasks
}

// pacingJSON 是心跳上报的生效节奏（取自配置；两个避让窗口写死，只报秒窗口开关）。
func (a *App) pacingJSON() string {
	cfg := a.opts.Config
	raw, _ := json.Marshal(map[string]any{
		"minute_window": cfg.Pacing.MinuteWindow, "request_interval": cfg.Pacing.RequestInterval.String(),
		"max_bytes_per_second": int64(cfg.Pacing.MaxBytesPerSecond), "max_concurrent_runs": cfg.Pacing.MaxConcurrentRuns,
		"max_storage_runs": cfg.Pacing.MaxStorageRuns,
	})
	return string(raw)
}

// ---- 本地跟随循环 ----

func (a *App) followLoop(ctx context.Context) {
	for {
		if a.opts.Sleep(ctx, a.untilNextFollow()) != nil {
			return
		}
		a.followOnce(ctx)
	}
}

// untilNextFollow 返回距离下一个每分钟第 AllowFromSecond 秒（25 秒）的时长，对齐秒窗口起点。
func (a *App) untilNextFollow() time.Duration {
	now := a.opts.Now().UTC()
	next := now.Truncate(time.Minute).Add(schedule.AllowFromSecond * time.Second)
	if !next.After(now) {
		next = next.Add(time.Minute)
	}
	return next.Sub(now)
}

func (a *App) syncedTaskRefs(ctx context.Context) []*archivepb.TaskRef {
	synced, err := a.opts.Staging.SyncedTasks(ctx)
	if err != nil {
		log.Warnf("archive worker list synced tasks failed: %v", err)
	}
	refs := make([]*archivepb.TaskRef, 0, len(synced))
	for _, t := range synced {
		refs = append(refs, &archivepb.TaskRef{SpaceId: t.SpaceID, DatasetId: t.DatasetID, TaskId: t.TaskID})
	}
	return refs
}

// followOnce 从 mgr 拉取当前全部启用且可跟随的任务，按最久未跟随优先顺序在并发与读 Storage 上限内执行一轮跟随并上报结果。
func (a *App) followOnce(ctx context.Context) {
	cfg := a.opts.Config
	a.mu.Lock()
	if a.maintaining || a.diskInsufficient || a.lastHeartbeatOK.IsZero() {
		a.mu.Unlock()
		return
	}
	a.followListing++
	a.mu.Unlock()
	listingDone := false
	finishListing := func() {
		if !listingDone {
			listingDone = true
			a.mu.Lock()
			a.followListing--
			a.mu.Unlock()
		}
	}
	defer finishListing()

	callCtx, cancel := context.WithTimeout(ctx, rpcTimeout)
	refs := a.syncedTaskRefs(callCtx)
	rsp, err := a.opts.Manager.ListFollowTasks(callCtx, &archivepb.ListFollowTasksReq{SyncedTasks: refs})
	cancel()
	if err != nil {
		log.Warnf("archive worker list follow tasks failed: %v", err)
		return
	}
	tasks := append([]*archivepb.FollowTask(nil), rsp.GetTasks()...)
	if len(tasks) == 0 {
		return
	}
	// 按上次本地跟随的开始时刻从早到晚排，未跟随过的排最前，防止长重读任务饿死其余短任务。
	a.mu.Lock()
	lastStart := make(map[string]time.Time, len(tasks))
	for _, task := range tasks {
		key := task.GetSpaceId() + "/" + task.GetDatasetId()
		lastStart[key] = a.lastFollowStart[key]
	}
	a.mu.Unlock()
	sort.Slice(tasks, func(i, j int) bool {
		ki := tasks[i].GetSpaceId() + "/" + tasks[i].GetDatasetId()
		kj := tasks[j].GetSpaceId() + "/" + tasks[j].GetDatasetId()
		ti, tj := lastStart[ki], lastStart[kj]
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		return ki < kj
	})

	var roundWG sync.WaitGroup
dispatch:
	for _, task := range tasks {
		key := task.GetSpaceId() + "/" + task.GetDatasetId()
		for {
			a.mu.Lock()
			if a.maintaining || a.diskInsufficient || ctx.Err() != nil {
				a.mu.Unlock()
				break dispatch
			}
			if _, busyFollow := a.following[key]; busyFollow || a.hasActiveRunLocked(key) {
				a.mu.Unlock()
				continue dispatch
			}
			totalBusy := len(a.running) + len(a.following) + a.pulling
			storageBusy := a.busyStorageRunsLocked()
			if totalBusy < cfg.Pacing.MaxConcurrentRuns && storageBusy < cfg.Pacing.MaxStorageRuns {
				followCtx, cancel := context.WithCancel(ctx)
				f := &activeFollow{task: task, cancel: cancel, ctx: followCtx}
				a.following[key] = f
				a.lastFollowStart[key] = a.opts.Now().UTC()
				a.mu.Unlock()
				roundWG.Add(1)
				a.launchFollow(key, f, &roundWG)
				continue dispatch
			}
			a.mu.Unlock()
			if a.waitSlot(ctx) != nil {
				break dispatch
			}
			// 等到槽位释放后，先让 pullOnce 试领一次排队中的手动/区间/导入运行，避免连续多轮重读饿死手动运行。
			a.pullOnce(ctx)
		}
	}
	finishListing()
	roundWG.Wait()
}

func (a *App) launchFollow(key string, f *activeFollow, roundWG *sync.WaitGroup) {
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer roundWG.Done()
		defer f.cancel()
		a.executeFollow(f.ctx, f)
		a.mu.Lock()
		delete(a.following, key)
		a.mu.Unlock()
		a.notifySlot()
	}()
}

// followAssignment 把 mgr 下发的 FollowTask 组装成 runner.Execute 使用的输入。
func followAssignment(task *archivepb.FollowTask, startedAt time.Time) *archivepb.RunAssignment {
	return &archivepb.RunAssignment{
		Kind:                    "follow",
		SpaceId:                 task.GetSpaceId(),
		DatasetId:               task.GetDatasetId(),
		TaskId:                  task.GetTaskId(),
		Purpose:                 task.GetPurpose(),
		DirName:                 task.GetDirName(),
		Freq:                    task.GetFreq(),
		PartitionUnit:           task.GetPartitionUnit(),
		Retention:               task.GetRetention(),
		AllowedLatenessSeconds:  task.GetAllowedLatenessSeconds(),
		SpaceTz:                 task.GetSpaceTz(),
		LocalClose:              task.GetLocalClose(),
		StagingRetentionSeconds: task.GetStagingRetentionSeconds(),
		StartedAt:               startedAt.UTC().UnixMilli(),
		BackfillFrom:            task.GetBackfillFrom(),
		DatasetMetaJson:         task.GetDatasetMetaJson(),
		LastSuccessAt:           task.GetLastSuccessAt(),
		RegisteredPartitions:    task.GetRegisteredPartitions(),
	}
}

func (a *App) executeFollow(ctx context.Context, f *activeFollow) {
	task := f.task
	release, err := a.acquireDataset(ctx, task.GetSpaceId(), task.GetDatasetId())
	if err != nil {
		return
	}
	defer release()
	startedAt := a.opts.Now().UTC()
	assignment := followAssignment(task, startedAt)
	report := func(runner.Progress) bool { return f.stop.Load() }
	outcome, execErr := a.opts.Runner.Execute(ctx, assignment, report, nil)
	req := &archivepb.ReportFollowResultReq{
		SpaceId:    task.GetSpaceId(),
		DatasetId:  task.GetDatasetId(),
		TaskId:     task.GetTaskId(),
		StartedAt:  startedAt.UnixMilli(),
		FinishedAt: a.opts.Now().UTC().UnixMilli(),
	}
	var reported []string
	if execErr != nil || outcome == nil {
		req.Status, req.Message = "failed", fmt.Sprintf("%v", execErr)
	} else {
		req.Status, req.Message = outcome.Status, outcome.Message
		req.Partitions, req.Unrecoverable = outcome.Partitions, outcome.Unrecoverable
		reported = outcome.ReportedKeys
		if !outcome.FinalUntil.IsZero() {
			req.FinalUntil = outcome.FinalUntil.UTC().Format(time.RFC3339)
		}
		if !outcome.RescanFrom.IsZero() && !outcome.RescanUntil.IsZero() {
			req.RescanFrom = outcome.RescanFrom.UTC().Format(time.RFC3339)
			req.RescanUntil = outcome.RescanUntil.UTC().Format(time.RFC3339)
		}
	}
	confirmed := a.reportFollow(ctx, req)
	if confirmed && len(reported) > 0 {
		// mgr 确认登记之后才清除待上报标记；上报失败时分区留在待上报里，下一轮跟随会再带上。
		if err := a.opts.Staging.MarkReported(context.WithoutCancel(ctx), task.GetSpaceId(), task.GetDatasetId(), reported); err != nil {
			log.Warnf("archive worker mark reported follow=%s/%s: %v", task.GetSpaceId(), task.GetDatasetId(), err)
		}
	}
}

// reportFollow 上报一轮本地跟随的结果：暂时失败时退避重试；mgr 返回冲突（任务重建、分区倒退）或不存在时放弃。返回 true 表示 mgr 已确认。
func (a *App) reportFollow(ctx context.Context, req *archivepb.ReportFollowResultReq) bool {
	for attempt := 0; attempt < completeRetries; attempt++ {
		callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rpcTimeout)
		_, err := a.opts.Manager.ReportFollowResult(callCtx, req)
		cancel()
		if err == nil {
			return true
		}
		if errors.Is(err, manager.ErrConflict) || errors.Is(err, manager.ErrNotFound) {
			log.Warnf("archive worker follow report rejected task=%s/%s#%s: %v", req.GetSpaceId(), req.GetDatasetId(), req.GetTaskId(), err)
			return false
		}
		log.Warnf("archive worker follow report failed task=%s/%s attempt=%d: %v", req.GetSpaceId(), req.GetDatasetId(), attempt+1, err)
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
	log.Errorf("archive worker gave up reporting follow task=%s/%s; partitions stay pending for the next round", req.GetSpaceId(), req.GetDatasetId())
	return false
}

// ---- 领取与执行（manual、range、import） ----

func (a *App) pullOnce(ctx context.Context) {
	cfg := a.opts.Config
	a.mu.Lock()
	free := cfg.Pacing.MaxConcurrentRuns - len(a.running) - len(a.following) - a.pulling
	blocked := a.maintaining || a.diskInsufficient
	busy := uint32(a.busyStorageRunsLocked())
	if free > 0 && !blocked && !a.lastHeartbeatOK.IsZero() {
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
	callCtx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	syncedRefs := a.syncedTaskRefs(callCtx)
	rsp, err := a.opts.Manager.Pull(callCtx, &archivepb.PullArchiveRunReq{
		FreeSlots:      uint32(free),
		MaxStorageRuns: uint32(cfg.Pacing.MaxStorageRuns), BusyStorageRuns: busy,
		SyncedTasks: syncedRefs,
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
		if assignment.GetKind() == "import" {
			run.batches, run.finished = make(chan runner.ImportBatch), make(chan struct{})
		}
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
		a.notifySlot()
	}()
}

// acquireDataset 等到同一个数据集没有其他执行（本地跟随或手动、补拉、导入运行）在本地进行；
// 手动运行等待期间由它的续租定时器照常续租，不会空耗租约。
func (a *App) acquireDataset(ctx context.Context, spaceID, datasetID string) (release func(), err error) {
	key := spaceID + "/" + datasetID
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

// execute 执行一次手动、补拉区间或导入运行并上报结果。
func (a *App) execute(ctx context.Context, run *activeRun) {
	assignment := run.assignment
	var latest atomic.Pointer[runner.Progress]
	latest.Store(&runner.Progress{})
	send := func() {
		p := latest.Load()
		callCtx, cancel := context.WithTimeout(ctx, rpcTimeout)
		rsp, err := a.opts.Manager.Progress(callCtx, &archivepb.ReportArchiveRunProgressReq{
			RunId: assignment.GetRunId(), LeaseToken: assignment.GetLeaseToken(),
			SubjectsDone: p.SubjectsDone, RequestCount: p.RequestCount, BytesRead: p.BytesRead, RowsRead: p.RowsRead,
		})
		cancel()
		switch {
		case err != nil:
			log.Warnf("archive worker progress report failed run=%s: %v", assignment.GetRunId(), err)
		case rsp.GetCancelled():
			run.stop.Store(true)
			run.cancel()
		}
	}
	// 续租与进度由独立的定时器负责，不依赖「读完一页」：等待本地跟随让出数据集、导出、定稿和整点避让等待期间也要续租。
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

	req := &archivepb.CompleteArchiveRunReq{RunId: assignment.GetRunId(), LeaseToken: assignment.GetLeaseToken()}
	var reported []string
	// 同数据集的串行保护延续到完成上报和确认清理之后：否则取消的旧运行或跟随的迟到上报可能覆盖新运行刚登记的分区。
	release, err := a.acquireDataset(ctx, assignment.GetSpaceId(), assignment.GetDatasetId())
	if err == nil {
		defer release()
	}
	if err != nil {
		req.Status, req.Message = "cancelled", "运行在等待同一数据集的上一次执行时被取消"
	} else {
		report := func(p runner.Progress) bool {
			cp := p
			latest.Store(&cp)
			return run.stop.Load()
		}
		var feed *runner.ImportFeed
		if run.batches != nil {
			feed = &runner.ImportFeed{Batches: run.batches, Finished: run.finished}
		}
		outcome, execErr := a.opts.Runner.Execute(ctx, assignment, report, feed)
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
			if !outcome.RescanFrom.IsZero() && !outcome.RescanUntil.IsZero() {
				req.RescanFrom = outcome.RescanFrom.UTC().Format(time.RFC3339)
				req.RescanUntil = outcome.RescanUntil.UTC().Format(time.RFC3339)
			}
		}
	}
	stopTick()
	confirmed := a.complete(ctx, req)
	if confirmed && len(reported) > 0 {
		// mgr 确认登记之后才清除待上报标记；上报失败时分区留在待上报里，下一次运行或跟随会再带上。
		if err := a.opts.Staging.MarkReported(context.WithoutCancel(ctx), assignment.GetSpaceId(), assignment.GetDatasetId(), reported); err != nil {
			log.Warnf("archive worker mark reported run=%s: %v", assignment.GetRunId(), err)
		}
	}
}

// importRun 找到正在执行的导入运行；不存在、不是导入运行、已停止或已收到 finish 时返回 ErrImportNotFound。
func (a *App) importRun(runID string) (*activeRun, bool, error) {
	a.mu.Lock()
	run, ok := a.running[runID]
	insufficient := a.diskInsufficient
	a.mu.Unlock()
	if !ok || run.batches == nil || run.stop.Load() || run.ctx.Err() != nil {
		return nil, false, fmt.Errorf("%w：%s", dataapi.ErrImportNotFound, runID)
	}
	select {
	case <-run.finished:
		return nil, false, fmt.Errorf("%w：%s 已收到结束通知", dataapi.ErrImportNotFound, runID)
	default:
	}
	return run, insufficient, nil
}

// Write 把一批行交给导入运行的协程写进暂存区，等它写完返回结果（数据端口 POST /v1/import/rows 调用）。
func (a *App) Write(ctx context.Context, runID string, req *storagepb.PrimaryUpsertFieldsReq) (dataapi.ImportResult, error) {
	run, insufficient, err := a.importRun(runID)
	if err != nil {
		return dataapi.ImportResult{}, err
	}
	if insufficient {
		return dataapi.ImportResult{}, dataapi.ErrDiskInsufficient
	}
	result := make(chan runner.ImportResult, 1)
	select {
	case run.batches <- runner.ImportBatch{Req: req, Result: result}:
	case <-run.ctx.Done():
		return dataapi.ImportResult{}, fmt.Errorf("%w：%s 已结束", dataapi.ErrImportNotFound, runID)
	case <-ctx.Done():
		return dataapi.ImportResult{}, ctx.Err()
	}
	select {
	case r := <-result:
		if errors.Is(r.Err, runner.ErrInvalidBatch) {
			return dataapi.ImportResult{}, fmt.Errorf("%w：%v", dataapi.ErrInvalidBatch, r.Err)
		}
		if r.Err != nil {
			return dataapi.ImportResult{}, r.Err
		}
		return dataapi.ImportResult{Added: r.Added, Changed: r.Changed, Same: r.Same}, nil
	case <-run.ctx.Done():
		return dataapi.ImportResult{}, fmt.Errorf("%w：%s 已结束", dataapi.ErrImportNotFound, runID)
	}
}

// Finish 通知导入运行批次已全部写完（数据端口 POST /v1/import/finish 调用），不等运行结束。
func (a *App) Finish(runID string) error {
	run, _, err := a.importRun(runID)
	if err != nil {
		return err
	}
	run.finishOnce.Do(func() { close(run.finished) })
	return nil
}

// complete 上报结果：暂时失败时退避重试；mgr 返回冲突（运行已结束或令牌失效）或不存在时放弃。返回 true 表示 mgr 已确认。
// 进程退出（ctx 取消）时用一个独立的短超时再试一次，让被取消的运行也能登记已导出的分区。
func (a *App) complete(ctx context.Context, req *archivepb.CompleteArchiveRunReq) bool {
	for attempt := 0; attempt < completeRetries; attempt++ {
		callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rpcTimeout)
		_, err := a.opts.Manager.Complete(callCtx, req)
		cancel()
		if err == nil {
			return true
		}
		if errors.Is(err, manager.ErrConflict) || errors.Is(err, manager.ErrNotFound) {
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
	log.Errorf("archive worker gave up reporting run=%s; the manager will mark it failed when its lease expires and the partitions stay pending for the next round", req.GetRunId())
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

// maintainOnce 执行一次维护：先停止跟随与领取并等进行中的执行结束，再发一次心跳取当前全部任务做对账
// （拿不到任务列表就跳过对账），最后回收暂存库空闲页并截断 WAL。
func (a *App) maintainOnce(ctx context.Context) {
	a.mu.Lock()
	a.maintaining = true
	a.mu.Unlock()
	a.notifySlot()
	defer func() {
		a.mu.Lock()
		a.maintaining = false
		a.mu.Unlock()
	}()
	deadline := a.opts.Now().Add(maintenanceWait)
	for a.busy() {
		if a.opts.Now().After(deadline) || ctx.Err() != nil {
			log.Warnf("archive worker maintenance skipped: runs or follows are still active")
			return
		}
		if a.opts.Sleep(ctx, 10*time.Second) != nil {
			return
		}
	}
	// 用这次心跳自己的结果：缓存的旧列表可能早已过时；只在对账时请求任务列表，常规 10s 心跳不查任务表。
	if keep := a.heartbeatOnce(ctx, true); keep == nil {
		log.Warnf("archive worker maintenance: 没有拿到当前任务列表，跳过对账")
	} else if dropped, err := a.opts.Staging.Reconcile(ctx, keep); err != nil {
		log.Warnf("archive worker reconcile staging: %v", err)
	} else if len(dropped) > 0 {
		log.Infof("archive worker dropped staging state of deleted tasks: %v", dropped)
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
		ProbeError: a.probeError, UnavailableSince: unixMilli(a.unavailableSince),
	}
}

// unixMilli 把时刻换成 Unix 毫秒，零时间给 0。
func unixMilli(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// Snapshot 是健康检查用的状态。
type Snapshot struct {
	Ready       bool
	Running     int
	Following   int
	Maintaining bool
	// DiskInsufficient：可用空间低于下限，已停止跟随与领取新运行。
	DiskInsufficient bool
	DiskFree         uint64
	LastError        string
}

// Snapshot 返回当前状态：最近一次心跳成功即就绪。
func (a *App) Snapshot() Snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return Snapshot{
		Ready:            !a.lastHeartbeatOK.IsZero() && a.opts.Now().Sub(a.lastHeartbeatOK) < 3*a.opts.Config.Worker.HeartbeatInterval,
		Running:          len(a.running),
		Following:        len(a.following),
		Maintaining:      a.maintaining,
		DiskInsufficient: a.diskInsufficient,
		DiskFree:         a.diskFree,
		LastError:        a.lastError,
	}
}
