package marketfetch

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/store"

	cloudnodepb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
	"trpc.group/trpc-go/trpc-go/log"
)

// DisableAll 停用该空间全部函数的定时器：已生效计划与未生效计划里出现过的函数都在内。
// 返回 true 表示云端已确认全部停用。
func (r *PlanReconciler) DisableAll(ctx context.Context) (bool, error) {
	if !r.mu.TryLock() {
		return false, fmt.Errorf("plan reconciliation already running")
	}
	defer r.mu.Unlock()
	names := map[string]string{}
	if applied, err := r.Store.LatestApplied(ctx, r.SpaceID); err != nil {
		return false, err
	} else if applied != nil {
		content, decodeErr := decodeFetchPlanContent(applied.Plan)
		if decodeErr != nil {
			return false, decodeErr
		}
		for name, function := range content.Functions {
			if function.IsSCF() {
				names[name] = function.Region
			}
		}
	}
	openRecord, err := r.Store.Open(ctx, r.SpaceID)
	if err != nil {
		return false, err
	}
	if open := openRecord; open != nil {
		content, decodeErr := decodeFetchPlanContent(open.Plan)
		if decodeErr != nil {
			return false, decodeErr
		}
		for name, function := range content.Functions {
			if function.IsSCF() {
				names[name] = function.Region
			}
		}
	}
	if len(names) == 0 {
		return true, nil
	}
	release, err := r.acquireOperation(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	// 先结算在途作业，再读回云端：若先读回，在途的启用作业可能在读回之后完成，用过期的 disabled 状态误判停用完成。
	busy := r.busyFunctions(ctx, true)
	// 停用确认必须以云端读回为准：目录缓存可能与云端实际状态不一致。
	nodes, err := r.Cloud.ListTimerMarketFetchersFresh(ctx, r.SpaceID)
	if err != nil {
		return false, err
	}
	var patches []*cloudnodepb.NodeRuntimeConfigPatch
	exists := map[string]scfinvoker.Node{}
	for _, node := range nodes {
		exists[node.NodeID] = node
	}
	lease, err := r.ensureLease(ctx)
	if err != nil {
		return false, err
	}
	deferred := 0
	for name := range names {
		if _, inFlight := busy[name]; inFlight {
			deferred++ // 仍有在途作业：本轮不提交，也不能宣布停用完成
			continue
		}
		node, ok := exists[name]
		if !ok {
			continue
		}
		if enabled, known := metadataBoolValue(node.Metadata, "timer_actual_enabled"); known && !enabled {
			continue
		}
		patches = append(patches, &cloudnodepb.NodeRuntimeConfigPatch{NodeId: name, TimerEnabled: false, TimerCron: "0 * * * * * *", CollectorPublishLeaseId: lease.LeaseID, CollectorPublishFencingToken: lease.FencingToken})
	}
	if err := r.invalidateConfirmations(ctx, openRecord); err != nil {
		return false, err
	}
	if len(patches) == 0 {
		r.releaseLease(ctx)
		return r.finishDisable(deferred)
	}
	// CloudNode 单个作业最多 100 项：分批提交，全部确认后才算停用完成。
	for start := 0; start < len(patches); start += runtimeConfigBatchSize {
		batch := patches[start:min(start+runtimeConfigBatchSize, len(patches))]
		jobID, err := r.Cloud.SubmitRuntimeConfigs(ctx, r.SpaceID, batch)
		if err != nil {
			return false, err
		}
		batchNames := make([]string, 0, len(batch))
		for _, patch := range batch {
			batchNames = append(batchNames, patch.GetNodeId())
		}
		items, terminal, err := r.waitAux(ctx, auxJobKindDisable, batchNames, jobID)
		if err != nil || !terminal {
			return false, err
		}
		for _, item := range items {
			if !item.Success {
				return false, fmt.Errorf("disable timer of %s: %s", item.NodeID, item.Message)
			}
		}
	}
	r.releaseLease(ctx)
	return r.finishDisable(deferred)
}

// EnableAll 启用已生效计划里全部函数的定时器，与 DisableAll 对称：恢复采集或进程重启后立即核对云端并补推，
// 不等对账逐轮刷新读回（读回过期的函数在对账里会被跳过，恢复会拖很多轮）。
// 返回 true 表示没有遗留项：没有在途作业，且需要启用的函数都已提交并确认。
func (r *PlanReconciler) EnableAll(ctx context.Context) (bool, error) {
	if !r.mu.TryLock() {
		return false, fmt.Errorf("plan reconciliation already running")
	}
	defer r.mu.Unlock()
	applied, err := r.Store.LatestApplied(ctx, r.SpaceID)
	if err != nil {
		return false, err
	}
	if applied == nil {
		return true, nil
	}
	content, err := decodeFetchPlanContent(applied.Plan)
	if err != nil {
		return false, err
	}
	functions, _ := content.Applied()
	functions = scfFunctions(functions)
	if len(functions) == 0 {
		return true, nil
	}
	// 启用定时器并下发环境之前，先让落后的函数跟上配置的代码包：环境的编码格式（例如采集计划）随代码版本变化，
	// 先启用会出现“旧代码读新环境”。升级失败或仍在进行都不启用，下一轮重试；没有落后函数时这一步什么也不做。
	if err := r.upgradeBeforeEnable(ctx); err != nil {
		if errors.Is(err, errCodeUpgradePending) {
			// 管理器随后直接跳过本 tick 的对账，唯一的收尾点不会执行：在这里补上，
			// 否则升级作业的 pending 指标不更新，之前的失败原因（例如租约被占用）也不会清除。
			r.finishRound(err)
		}
		return false, err
	}
	release, err := r.acquireOperation(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	busy := r.busyFunctions(ctx, true)
	nodes, err := r.Cloud.ListTimerMarketFetchersFresh(ctx, r.SpaceID)
	if err != nil {
		return false, err
	}
	exists := map[string]scfinvoker.Node{}
	for _, node := range nodes {
		exists[node.NodeID] = node
	}
	names := make([]string, 0, len(functions))
	for name := range functions {
		names = append(names, name)
	}
	sort.Strings(names)
	deferred := 0
	var patches []*cloudnodepb.NodeRuntimeConfigPatch
	for _, name := range names {
		function := functions[name]
		if function.Cron == "" {
			continue
		}
		if _, inFlight := busy[name]; inFlight {
			deferred++
			continue
		}
		node, ok := exists[name]
		if !ok || !timerNeedsRepair(node.Metadata, function.Cron) {
			continue // 缺失的函数交给对账重建
		}
		// 启用定时器的配置必须带受管环境变量（云端校验 MOOX_FETCH_TIMEOUT_SECONDS 等），与对账修复用同一份。
		environment, envErr := r.functionEnvironment(function.Region, function.Plan)
		if envErr != nil {
			return false, fmt.Errorf("function %s: %w", name, envErr)
		}
		patches = append(patches, &cloudnodepb.NodeRuntimeConfigPatch{NodeId: name, ManagedEnvironment: environment, TimerEnabled: true, TimerCron: function.Cron})
	}
	if len(patches) == 0 {
		r.lastVerified = time.Time{}
		return deferred == 0, nil
	}
	lease, err := r.ensureLease(ctx)
	if err != nil {
		return false, err
	}
	allOK := deferred == 0
	for start := 0; start < len(patches); start += runtimeConfigBatchSize {
		batch := patches[start:min(start+runtimeConfigBatchSize, len(patches))]
		batchNames := make([]string, 0, len(batch))
		for _, patch := range batch {
			patch.CollectorPublishLeaseId, patch.CollectorPublishFencingToken = lease.LeaseID, lease.FencingToken
			batchNames = append(batchNames, patch.GetNodeId())
		}
		jobID, err := r.Cloud.SubmitRuntimeConfigs(ctx, r.SpaceID, batch)
		if err != nil {
			return false, err
		}
		items, terminal, err := r.waitAux(ctx, auxJobKindRepair, batchNames, jobID)
		if err != nil || !terminal {
			return false, err
		}
		for _, item := range items {
			if !item.Success {
				allOK = false
				log.WarnContextf(ctx, "collector fetch plan enable timer of %s failed: %s", item.NodeID, item.Message)
			}
		}
	}
	r.releaseLease(ctx)
	r.lastVerified = time.Time{} // 启用之后让下一轮对账立即读回校验
	return allOK, nil
}

// invalidateConfirmations 作废未完成计划里的下发与收尾确认（保留 Created/Registered 与原 P）：停用之后这些确认不再代表云端状态，
// 重新启用时按原计划重推（下发是幂等的）。必须在提交停用之前持久化：否则部分停用后崩溃，重启时空间已重新启用，
// 旧确认会让计划一直跳过这些函数。
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

// finishDisable 在停用确认后收尾：仍有在途作业则未完成；否则让已生效函数在重新启用后立即校验。
func (r *PlanReconciler) finishDisable(deferred int) (bool, error) {
	if deferred > 0 {
		return false, nil
	}
	r.lastVerified = time.Time{}
	return true, nil
}

// PlanManager 为每个启用采集计划的空间维持一个独立的对账协程，每分钟第 Offset 秒执行一轮。
// 空间不再启用时，先停用其全部函数的定时器，确认后才退出该空间的协程。
type PlanManager struct {
	// Spaces 返回当前应当运行对账的空间。
	Spaces func(ctx context.Context) ([]string, error)
	// Configured 是配置了采集计划的全部空间。其中当前不在 Spaces 里的空间（停用或在停机期间被停用）
	// 会先做一次停用收尾，确保旧的定时器不再运行；确认后不再重复。
	Configured []string
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
}

type planWorker struct {
	cancel context.CancelFunc
	stop   chan struct{}
	done   chan struct{}
	// disabled 在协程因停用收尾完成而退出时置位（done 关闭后读取）。
	disabled bool
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
		}
	}
}

func (m *PlanManager) sync(ctx context.Context) {
	spaces, err := m.Spaces(ctx)
	if err != nil {
		log.WarnContextf(ctx, "collector fetch plan space list failed: %v", err)
		return
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
		worker := &planWorker{cancel: cancel, stop: make(chan struct{}, 1), done: make(chan struct{})}
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
	for _, space := range m.Configured {
		if _, active := wanted[space]; !active && !m.disabled[space] {
			start(space, true)
		}
	}
}

// runSpace 是一个空间的对账协程；上一轮未结束时下一轮自然被推迟，不会重叠。
func (m *PlanManager) runSpace(ctx context.Context, space string, reconciler *PlanReconciler, worker *planWorker) {
	defer m.wg.Done()
	defer close(worker.done)
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

// errCodeUpgradePending 表示仍有函数运行着比配置更旧的代码包：此时不能启用定时器，也不能下发新环境。
var errCodeUpgradePending = errors.New("function code upgrade pending")

// upgradeBeforeEnable 刷新配置的代码包并升级落后的函数；调用方已持有 r.mu。
// 升级作业是异步的、失败也只记日志，所以最后要读回云端确认没有任何函数仍在旧代码包上，
// 否则返回 errCodeUpgradePending，调用方本轮既不启用定时器也不继续对账。
func (r *PlanReconciler) upgradeBeforeEnable(ctx context.Context) error {
	if err := r.refreshPackage(ctx); err != nil {
		return r.fail("package", err)
	}
	upgradeCtx, cancel := upgradeContext(ctx)
	upgradeErr := r.upgradeLaggingFunctions(upgradeCtx)
	cancel()
	if upgradeErr != nil {
		log.WarnContextf(ctx, "collector fetch plan code upgrade before enabling timers space=%s failed: %v", r.SpaceID, upgradeErr)
	}
	pkg := strings.TrimSpace(r.packageID())
	if pkg == "" {
		return nil
	}
	nodes, err := r.Cloud.ListTimerMarketFetchersFresh(ctx, r.SpaceID)
	if err != nil {
		// 读不到云端就无法确认代码已经升级：同样不放行，等下一轮。这是 CloudNode 调用失败，必须记入失败，
		// 否则 EnableAll 里按“升级等待”收尾时会把它当成无失败而清掉既有的失败原因。
		return r.fail("cloudnode", fmt.Errorf("%w: cannot read back the fleet: %v", errCodeUpgradePending, err))
	}
	prefix := "moox-fetch" + PlanFleetMarker + planNameSanitizer.Replace(strings.ToLower(r.SpaceID)) + "-"
	stale := 0
	for _, node := range nodes {
		if !strings.HasPrefix(node.NodeID, prefix) || strings.TrimSpace(node.PackageID) == "" || node.PackageID == pkg {
			continue
		}
		// 云端已确认不存在的函数没有代码可升级，交给对账清理和重建，不能因此永久阻塞整个空间。
		if readbackState(node.Metadata, r.now()) == readbackGone {
			continue
		}
		stale++
	}
	if stale > 0 {
		return fmt.Errorf("%w: %d functions are still on an older code package", errCodeUpgradePending, stale)
	}
	return nil
}
