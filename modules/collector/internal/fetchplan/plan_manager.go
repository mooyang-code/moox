package fetchplan

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/cloud"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"trpc.group/trpc-go/trpc-go/log"
)

// DisableAll 停用该空间全部定时函数的定时器（已生效计划与未生效计划里出现过的都在内），返回 true 表示云上已确认全部停用。
// 顺序写死（暂停后不能有后台操作再把定时器打开，重置的第一步 verify_scf_timers_disabled 靠这个信号）：
//
//  1. 把空间标成“正在暂停”：之后操作表拒绝为它起可能打开定时器的操作（定时函数的 Ensure、启用定时器）；
//     补采函数与删除照常放行（恢复追赶时系统暂停还开着，补采却要跑）。
//  2. 取消还没拿到并发名额的这类操作。
//  3. 等正在跑的这类操作结束（不取消，避免留下建到一半的函数）；还没结束就返回未完成，空间协程每分钟重试。
//  4. 清掉这些操作留下的结果，免得恢复后被当成某个阶段的确认。
//  5. 作废未生效计划里的下发与收尾确认并持久化。
//  6. 对每个定时函数起“停用定时器”的操作并等它们结束。
//  7. 读回全部定时函数，确认定时器都是停用，才宣布完成。
func (r *PlanReconciler) DisableAll(ctx context.Context) (bool, error) {
	if !r.mu.TryLock() {
		return false, fmt.Errorf("plan reconciliation already running")
	}
	defer r.mu.Unlock()
	r.pausing.Store(true)
	if running := r.ops().quiesceTimerCapable(); running > 0 {
		log.InfoContextf(ctx, "collector fetch plan disabling waits for running cloud operations space=%s running=%d", r.SpaceID, running)
		return false, nil
	}
	refs, err := r.timerFunctionRefs(ctx)
	if err != nil {
		return false, err
	}
	openRecord, err := r.Store.Open(ctx, r.SpaceID)
	if err != nil {
		return false, err
	}
	if err := r.invalidateConfirmations(ctx, openRecord); err != nil {
		return false, err
	}
	if len(refs) == 0 {
		return r.finishDisable(), nil
	}
	ops := r.ops()
	deferred := 0
	var started []*functionOp
	var firstErr error
	for _, name := range sortedKeys(refs) {
		// 上一次调用起的停用已经成功结束：不再起（第 7 步的读回会确认）；失败了就重起。
		if done := ops.take(name, opKeyDisable); done != nil && done.err == nil {
			continue
		}
		op, err := r.startDisableTimer(refs[name], opKeyDisable)
		if err != nil {
			deferred++ // 别的操作（删除、补采）在跑：等它结束
			continue
		}
		started = append(started, op)
	}
	ops.wait(ctx, started, r.roundWait(ctx))
	for _, op := range started {
		done := ops.take(op.name, opKeyDisable)
		switch {
		case done == nil:
			deferred++
		case done.err != nil && firstErr == nil:
			firstErr = fmt.Errorf("disable timer of %s: %w", op.name, done.err)
		}
	}
	if firstErr != nil {
		return false, firstErr
	}
	if deferred > 0 {
		return false, nil
	}
	for _, name := range sortedKeys(refs) {
		state, err := r.describe(ctx, refs[name])
		if err != nil {
			return false, fmt.Errorf("read back timer of %s: %w", name, err)
		}
		if state.Exists && state.Timer != nil && state.Timer.Enabled {
			log.WarnContextf(ctx, "collector fetch plan timer of %s is still enabled after disabling space=%s", name, r.SpaceID)
			return false, nil
		}
	}
	return r.finishDisable(), nil
}

// EnableAll 启用已生效计划里全部函数的定时器，与 DisableAll 对称：恢复采集或进程重启后立即核对云端并补上，
// 不等对账逐轮读回。启用之前先让落后的函数跟上最新的代码包（环境的编码格式随代码版本变化，先启用会出现“旧代码读新环境”）：
// 有函数还在旧代码包上就先起 UpgradeCode，升级没成功之前返回 errCodeUpgradePending，一个都不启用。
// 返回 true 表示没有遗留项：没有在跑的操作，且需要启用的函数都已确认。缺失的函数交给对账重建。
func (r *PlanReconciler) EnableAll(ctx context.Context) (bool, error) {
	if !r.mu.TryLock() {
		return false, fmt.Errorf("plan reconciliation already running")
	}
	defer r.mu.Unlock()
	targets, err := r.appliedTargets(ctx)
	if err != nil {
		return false, err
	}
	if len(targets) == 0 {
		return true, nil
	}
	r.refreshPackageIfAvailable(ctx)
	if err := r.packageGate(ctx); err != nil {
		return false, err
	}
	pkg := strings.TrimSpace(r.packageID())
	ops := r.ops()
	deferred := 0
	states := map[string]cloud.FunctionState{}
	var lagging []*functionOp
	for _, name := range sortedKeys(targets) {
		ref := r.functionRef(name, targets[name].Region)
		if ops.busy(name) {
			deferred++
			continue
		}
		state, err := r.describe(ctx, ref)
		if err != nil {
			return false, r.fail("cloud", fmt.Errorf("%w: cannot read back %s: %v", errCodeUpgradePending, name, err))
		}
		if !state.Exists {
			continue // 缺失的函数交给对账重建
		}
		states[name] = state
		if pkg == "" || state.PackageID == pkg {
			continue
		}
		var fallback *cloud.FunctionSpec
		if spec, err := r.desiredSpec(targets[name]); err == nil {
			fallback = &spec
		}
		if op, err := r.startUpgrade(ref, fallback); err == nil {
			lagging = append(lagging, op)
		} else {
			deferred++
		}
	}
	if len(lagging) > 0 {
		ops.wait(ctx, lagging, r.roundWait(ctx))
		stale := 0
		for _, op := range lagging {
			done := ops.take(op.name, opKeyUpgrade)
			if done == nil || done.err != nil {
				stale++
				if done != nil {
					// 升级失败要报出来：这期间整个空间都不启用定时器，不报的话 Monitor 看不到。
					_ = r.opFailure(fmt.Errorf("code upgrade of %s before enabling timers: %w", op.name, done.err))
				}
			}
		}
		if stale > 0 {
			err := fmt.Errorf("%w: %d functions are still on an older code package", errCodeUpgradePending, stale)
			// 管理器随后直接跳过本 tick 的对账，唯一的收尾点不会执行：在这里补上。
			r.finishRound(err)
			return false, err
		}
	}
	var started []*functionOp
	for _, name := range sortedKeys(states) {
		target := targets[name]
		if target.Cron == "" || TimerMatches(states[name].Timer, target.Cron) {
			continue
		}
		op, err := r.ops().start(name, opKeyEnable, true, func(ctx context.Context, _ *functionOp) error {
			return r.Cloud.SetTimer(ctx, r.functionRef(name, target.Region), cloud.Timer{Cron: target.Cron, Enabled: true}, pkg)
		})
		if err != nil {
			deferred++
			continue
		}
		started = append(started, op)
	}
	ops.wait(ctx, started, r.roundWait(ctx))
	allOK := deferred == 0
	for _, op := range started {
		done := ops.take(op.name, opKeyEnable)
		if done == nil {
			allOK = false
			continue
		}
		if done.err != nil {
			allOK = false
			log.WarnContextf(ctx, "collector fetch plan enable timer of %s failed: %v", op.name, done.err)
		}
	}
	r.lastVerified = time.Time{} // 启用之后让下一轮对账立即读回校验
	return allOK, nil
}

// appliedTargets 返回最近一份已生效计划里的定时函数。
func (r *PlanReconciler) appliedTargets(ctx context.Context) (map[string]functionTarget, error) {
	applied, err := r.Store.LatestApplied(ctx, r.SpaceID)
	if err != nil || applied == nil {
		return nil, err
	}
	content, err := decodeFetchPlanContent(applied.Plan)
	if err != nil {
		return nil, err
	}
	functions, _ := content.Applied()
	targets := map[string]functionTarget{}
	for name, function := range scfFunctions(functions) {
		targets[name] = functionTarget{Name: name, Region: function.Region, Plan: function.Plan, Cron: function.Cron}
	}
	return targets, nil
}

// invalidateConfirmations 作废未完成计划里的下发与收尾确认（保留 Registered 与原 P）：停用之后这些确认不再代表云端状态，
// 重新启用时按原计划重推（Ensure 是幂等的）。必须在停用定时器之前持久化：否则部分停用后崩溃，重启时空间已重新启用，
// 旧确认会让计划一直跳过这些函数，计划里新建的函数恢复后一直停用。
func (r *PlanReconciler) invalidateConfirmations(ctx context.Context, openRecord *store.FetchPlanRecord) error {
	if openRecord == nil {
		return nil
	}
	progress, err := decodeFetchPlanProgress(openRecord.Progress)
	if err != nil {
		return err
	}
	if len(progress.Transitioned) == 0 && len(progress.Finalized) == 0 {
		return nil
	}
	progress.Transitioned, progress.Finalized = nil, nil
	if err := r.saveProgress(ctx, openRecord, progress, openRecord.Status, openRecord.LastError); err != nil {
		return err
	}
	openRecord.Progress = mustJSON(progress)
	return nil
}

// finishDisable 在停用确认后收尾：让已生效函数在重新启用后立即校验。
func (r *PlanReconciler) finishDisable() bool {
	r.lastVerified = time.Time{}
	return true
}

// PlanManager 为每个启用采集计划的空间维持一个独立的对账协程，每分钟第 Offset 秒执行一轮。
// 空间不再启用时，先停用其全部函数的定时器，确认后才退出该空间的协程。
type PlanManager struct {
	// Spaces 返回当前应当运行对账的空间。
	Spaces func(ctx context.Context) ([]string, error)
	// Configured 返回管理集合：有启用任务、有采集计划、配置了云函数或处于暂停的空间。其中当前不在 Spaces 里的空间
	// （停用、暂停或在停机期间被停用）会先做一次停用收尾，确保旧的定时器不再运行；确认后不再重复。
	Configured func(ctx context.Context) ([]string, error)
	// Factory 为空间创建对账器。
	Factory  func(spaceID string) (*PlanReconciler, error)
	Interval time.Duration
	Offset   time.Duration
	// Timeout 是单轮对账的超时。
	Timeout time.Duration
	// AfterTick 在每轮对账之后调用（推进补采作业、自动补采扫描），错误只记录日志。
	AfterTick func(ctx context.Context, spaceID string) error

	mu       sync.Mutex
	workers  map[string]*planWorker
	disabled map[string]bool
	wg       sync.WaitGroup

	triggerOnce sync.Once
	triggered   chan struct{}
	pending     map[string]bool
}

type planWorker struct {
	reconciler *PlanReconciler
	cancel     context.CancelFunc
	stop       chan struct{}
	// wake 让协程立刻跑一轮，容量为 1，多次触发合并成一次。
	wake chan struct{}
	done chan struct{}
	// disabled 在协程因停用收尾完成而退出时置位（done 关闭后读取）。
	disabled bool
}

func (m *PlanManager) triggerSignal() chan struct{} {
	m.triggerOnce.Do(func() { m.triggered = make(chan struct{}, 1) })
	return m.triggered
}

// Trigger 让空间尽快对账一轮，不等下一个周期。这里只做通知：同步空间要用 Run 的上下文，
// 空间协程从它派生，用调用方的上下文的话请求一结束协程就退出了。
func (m *PlanManager) Trigger(space string) {
	m.mu.Lock()
	if m.pending == nil {
		m.pending = map[string]bool{}
	}
	m.pending[space] = true
	m.mu.Unlock()
	select {
	case m.triggerSignal() <- struct{}{}:
	default:
	}
}

// wakeTriggered 唤醒被触发的空间协程。
func (m *PlanManager) wakeTriggered() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for space := range m.pending {
		for name, worker := range m.workers {
			if strings.EqualFold(name, space) {
				select {
				case worker.wake <- struct{}{}:
				default:
				}
			}
		}
	}
	m.pending = nil
}

// TimersDisabled 报告空间的对账协程是否已在停用收尾确认后退出：此时云端已确认该空间全部函数的定时器停用，
// 并且不会再启动，直到空间重新出现在 Spaces 里。
func (m *PlanManager) TimersDisabled(space string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, disabled := range m.disabled {
		if disabled && strings.EqualFold(name, space) {
			return true
		}
	}
	return false
}

// PlanIssue 返回空间最近一轮对账没有建出计划的原因；没有运行中的协程或没有问题时为空。
func (m *PlanManager) PlanIssue(space string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, worker := range m.workers {
		if strings.EqualFold(name, space) {
			return worker.reconciler.Issue()
		}
	}
	return ""
}

func (m *PlanManager) interval() time.Duration {
	if m.Interval > 0 {
		return m.Interval
	}
	return time.Minute
}

func (m *PlanManager) offset() time.Duration {
	if m.Offset > 0 {
		return m.Offset
	}
	return 10 * time.Second
}

func (m *PlanManager) timeout() time.Duration {
	if m.Timeout > 0 {
		return m.Timeout
	}
	return 55 * time.Second
}

// untilNext 返回距离下一个“整分钟 + Offset”的时间。
func untilNext(now time.Time, interval, offset time.Duration) time.Duration {
	base := now.Truncate(interval).Add(offset)
	if !base.After(now) {
		base = base.Add(interval)
	}
	return base.Sub(now)
}

// Run 阻塞运行直到 ctx 结束：每个周期同步一次空间集合，并等待所有协程退出。
func (m *PlanManager) Run(ctx context.Context) {
	m.sync(ctx)
	timer := time.NewTimer(untilNext(time.Now(), m.interval(), 0))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			m.wg.Wait()
			return
		case <-timer.C:
			m.sync(ctx)
			timer.Reset(untilNext(time.Now(), m.interval(), 0))
		case <-m.triggerSignal():
			m.sync(ctx)
			m.wakeTriggered()
		}
	}
}

func (m *PlanManager) sync(ctx context.Context) {
	spaces, err := m.Spaces(ctx)
	if err != nil {
		log.WarnContextf(ctx, "collector fetch plan space list failed: %v", err)
		return
	}
	var configured []string
	if m.Configured != nil {
		if configured, err = m.Configured(ctx); err != nil {
			log.WarnContextf(ctx, "collector fetch plan managed spaces failed: %v", err)
			return
		}
	}
	wanted := make(map[string]struct{}, len(spaces))
	for _, space := range spaces {
		wanted[space] = struct{}{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.workers == nil {
		m.workers = map[string]*planWorker{}
	}
	if m.disabled == nil {
		m.disabled = map[string]bool{}
	}
	for space := range wanted {
		delete(m.disabled, space)
	}
	for space, worker := range m.workers {
		select {
		case <-worker.done:
			if worker.disabled {
				m.disabled[space] = true
			}
			delete(m.workers, space)
			continue
		default:
		}
		if _, ok := wanted[space]; !ok {
			select {
			case worker.stop <- struct{}{}:
			default:
			}
		}
	}
	start := func(space string, stopping bool) {
		if _, running := m.workers[space]; running {
			return
		}
		reconciler, err := m.Factory(space)
		if err != nil {
			log.WarnContextf(ctx, "collector fetch plan reconciler for space %s is unavailable: %v", space, err)
			return
		}
		workerCtx, cancel := context.WithCancel(ctx)
		worker := &planWorker{reconciler: reconciler, cancel: cancel, stop: make(chan struct{}, 1), wake: make(chan struct{}, 1), done: make(chan struct{})}
		if stopping {
			worker.stop <- struct{}{}
		}
		m.workers[space] = worker
		m.wg.Add(1)
		go m.runSpace(workerCtx, space, reconciler, worker)
	}
	for space := range wanted {
		start(space, false)
	}
	for _, space := range configured {
		if _, active := wanted[space]; !active && !m.disabled[space] {
			start(space, true)
		}
	}
}

// runSpace 是一个空间的对账协程；上一轮未结束时下一轮自然被推迟，不会重叠。
func (m *PlanManager) runSpace(ctx context.Context, space string, reconciler *PlanReconciler, worker *planWorker) {
	defer m.wg.Done()
	defer close(worker.done)
	// 协程退出（暂停收尾完成、进程退出）时取消并等待本空间在跑的云端操作；操作表本身照常可用，暂停期间补采函数与删除还要用。
	defer reconciler.StopOperations(false)
	stopping := false
	// 对账协程每次启动（恢复采集、进程重启）后先把已生效计划里的定时器全部启用，成功前每轮重试。
	enabling := true
	for {
		timer := time.NewTimer(untilNext(time.Now(), m.interval(), m.offset()))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-worker.stop:
			stopping = true
			timer.Stop()
		case <-worker.wake:
			timer.Stop()
			// stop 和 wake 同时就绪时 select 是随机选的，停用中的空间不能再跑一轮启用。
			select {
			case <-worker.stop:
				stopping = true
			default:
			}
		case <-timer.C:
		}
		tickCtx, cancel := context.WithTimeout(ctx, m.timeout())
		if stopping {
			done, err := reconciler.DisableAll(tickCtx)
			cancel()
			if err != nil {
				log.WarnContextf(ctx, "collector fetch plan disabling space=%s failed: %v", space, err)
				continue
			}
			if done {
				log.InfoContextf(ctx, "collector fetch plan space stopped space=%s", space)
				worker.disabled = true
				return
			}
			continue
		}
		if enabling {
			done, err := reconciler.EnableAll(tickCtx)
			if errors.Is(err, errCodeUpgradePending) {
				// 函数代码还没升级完：对账会把新环境和定时器启用推下去，旧代码读不了新环境，本轮整体跳过。
				log.WarnContextf(ctx, "collector fetch plan waiting for function code upgrade space=%s: %v", space, err)
				cancel()
				continue
			}
			if err != nil {
				log.WarnContextf(ctx, "collector fetch plan enabling timers space=%s failed: %v", space, err)
			} else if done {
				enabling = false
			}
		}
		if err := reconciler.Reconcile(tickCtx); err != nil {
			log.WarnContextf(ctx, "collector fetch plan reconciliation failed space=%s: %v", space, err)
		}
		if m.AfterTick != nil {
			// 补采推进与对账各用各的超时，对账等云端作业久了不会饿死补采。
			followCtx, followCancel := context.WithTimeout(ctx, 30*time.Second)
			err := m.AfterTick(followCtx, space)
			followCancel()
			if err != nil {
				log.WarnContextf(ctx, "collector fetch plan follow-up failed space=%s: %v", space, err)
			}
		}
		cancel()
	}
}

// errCodeUpgradePending 表示仍有函数运行着比最新代码包更旧的代码：此时不能启用定时器，也不能下发新环境。
var errCodeUpgradePending = errors.New("function code upgrade pending")
