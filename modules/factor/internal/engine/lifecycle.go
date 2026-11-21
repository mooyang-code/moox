package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/localdata"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"trpc.group/trpc-go/trpc-go/log"
)

// 引擎的生命周期（设计 3.8）：模块级只有运行、暂停、恢复追赶三种状态；目录里的因子任务没有自己的阶段，
// 每个任务计算前要过一组检查（checkTask），检查通过才计算，不通过就在任务状态里写明哪一项没过。
// 检查每 30 秒重查一次，窗口载入完成时立即重查；窗口没载入时自动唤醒预载，不需要任何命令。

// newLifecycle 创建引擎的生命周期控制器。
func (r *Runtime) newLifecycle() (*lifecycle.Controller, error) {
	return lifecycle.New(lifecycle.Options{
		Audit:     func(event string) { log.Infof("%s", event) },
		Module:    "factor-engine",
		StateFile: r.cfg.Lifecycle.StateFile,
		Hooks: lifecycle.Hooks{
			Catchup: r.catchupHook,
			Quiesce: r.quiesce,
			OnChange: func(from, to lifecycle.Stage, reason string) {
				if to == lifecycle.StageResuming {
					// 恢复一开始就同步目录：追赶的前置条件要等本次恢复之后的同步。
					r.syncer.SyncSoon()
				}
				log.Infof("factor_lifecycle_changed from=%s to=%s reason=%s", from, to, reason)
			},
		},
	})
}

// windowCheckName、modulesCheckName 是“本地窗口已载入”“自定义算子模块就绪”两项检查的名字：恢复追赶据此区分暂时不通过与别的不通过项。
const (
	windowCheckName  = "本地窗口"
	modulesCheckName = "自定义算子模块"
)

// taskChecks 缓存各任务最近一次检查的结果。没有检查过的任务算不通过：引擎启动后窗口载入、检查通过之前不出结果。
type taskChecks struct {
	mu     sync.Mutex
	byTask map[string][]domain.TaskCheck
}

func (t *taskChecks) get(taskID string) ([]domain.TaskCheck, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	checks, ok := t.byTask[taskID]
	return checks, ok
}

func (t *taskChecks) put(taskID string, checks []domain.TaskCheck) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.byTask == nil {
		t.byTask = map[string][]domain.TaskCheck{}
	}
	t.byTask[taskID] = checks
}

// keep 只保留目录里还有的任务。
func (t *taskChecks) keep(taskIDs []string) {
	keep := make(map[string]struct{}, len(taskIDs))
	for _, id := range taskIDs {
		keep[id] = struct{}{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for id := range t.byTask {
		if _, ok := keep[id]; !ok {
			delete(t.byTask, id)
		}
	}
}

// taskReady 报告任务能不能计算：引擎是活动引擎（目录已载入、没有租约冲突）、窗口已载入、最近一次检查没有不通过项。
// 窗口直接看预载器：目录变化触发重新预载时，旧的“通过”不能在下一次重查之前放行计算。
func (r *Runtime) taskReady(taskID string) bool {
	if !r.active() || !r.preload.Ready(taskID) {
		return false
	}
	checks, ok := r.checks.get(taskID)
	return ok && domain.TaskChecksPassed(checks)
}

// gate 是实时周期处理前的门禁：模块在运行、任务检查通过才计算。
func (r *Runtime) gate(taskID string) bool { return r.life.Running() && r.taskReady(taskID) }

// catchupGate 是恢复追赶的门禁：只写历史数据（连续水位追赶、历史周期补算），只在模块恢复追赶中、任务检查通过时放行。
// 除此之外还要满足追赶前置条件：本次恢复之后至少成功同步过一次目录（见 catchupPrecondition），否则可能先用暂停前的旧目录写出结果。
func (r *Runtime) catchupGate(taskID string) bool {
	return r.life.Stage() == lifecycle.StageResuming && r.taskReady(taskID) && r.catchupPrecondition(taskID) == ""
}

// resumeStartedAt 是本次恢复追赶开始的时间；不在恢复追赶时为零。
func (r *Runtime) resumeStartedAt() time.Time {
	if state := r.life.State(); state.Stage == lifecycle.StageResuming {
		return state.Since
	}
	return time.Time{}
}

// catchupPrecondition 返回一个任务的追赶前置条件未满足的原因，满足时为空（见 catchupPreconditionFor）。
func (r *Runtime) catchupPrecondition(taskID string) string {
	var tasks []domain.EngineTask
	if r.catalog != nil {
		if set, lookup := r.catalog.SetByID(taskID); lookup == SetFound {
			tasks = []domain.EngineTask{set}
		}
	}
	return r.catchupPreconditionFor(tasks)
}

// catchupPreconditionFor 返回追赶前置条件未满足的原因，满足时为空。追赶在写任何结果（推进水位、后台补算）之前都要满足：
// tasks 引用的自定义算子模块全部就绪，并且本次恢复开始之后目录至少成功同步过一次（返回已应用或未变化都算）。
// 模块级恢复只核对参与本次追赶的任务（catchupMembers）：被排除的任务不计算，它用到的算子编译失败不挡放行。
// 没有目录同步器的运行时（测试）不要求目录同步。
func (r *Runtime) catchupPreconditionFor(tasks []domain.EngineTask) string {
	started := r.resumeStartedAt()
	if started.IsZero() {
		return ""
	}
	// 追赶写结果之前，引用的自定义算子模块必须全部就绪：不会拿旧版本、或还没编译好的版本计算。
	if reason := r.catchupModulesPending(tasks); reason != "" {
		return reason
	}
	if r.syncer == nil {
		return ""
	}
	if !r.syncer.SyncedSince().Before(started) {
		return ""
	}
	r.syncer.SyncSoon()
	return "等待本次恢复之后的目录同步（管理端不可达时会一直等待）"
}

// catchupModulesPending 返回 tasks 引用的自定义算子模块还没有全部就绪的原因，就绪或没有引用时为空。
func (r *Runtime) catchupModulesPending(tasks []domain.EngineTask) string {
	if r.udf == nil {
		return ""
	}
	for _, ref := range refsOfTasks(tasks) {
		if !r.udf.Ready(ref.hash) {
			return fmt.Sprintf("自定义算子 %s@%.12s 的模块还没有就绪（拉取或编译中、或失败，见引擎状态的模块列表）", ref.name, ref.hash)
		}
	}
	return ""
}

// catchupMembers 是参与本次恢复追赶的任务：有不会自己好的不通过项的任务不参与（本来就不计算，不挡放行，
// 检查通过之后由连续水位从断点补算）。暂时不通过的照常参与，在它们好之前报未追平：
//   - 窗口没载入（例如暂停期间引擎重启，载入记录只在内存里）：追赶会先同步本地数据，载入窗口并立即重查；
//   - 引用的自定义算子模块还没开始拉取、正在拉取或编译：模块就绪后重查通过；拉取失败、编译失败要人工处理，才排除。
//
// 还没检查过的任务也参与，同步之后的重查决定它去留。任务不能计算（本地数据、回看数量、变更日志断档，见 taskError）
// 按当时的状态直接判断，不等下一次重查：这些任务不能再建追赶目标、推进水位。
func (r *Runtime) catchupMembers() []domain.EngineTask {
	if r.catalog == nil {
		return nil
	}
	var members []domain.EngineTask
	for _, set := range r.catalog.Sets() {
		checks, _ := r.checks.get(set.Set.TaskID)
		if failsBeyondWindow(checks) || r.modulesFailed(set) || r.taskError(set) != nil {
			continue
		}
		members = append(members, set)
	}
	return members
}

// taskError 返回任务当前不能计算的原因，能计算时为 nil；与 blockedCheck 用同一个判断：
// 任务自己的因子回看超过 N（“回看数量”，只影响这个任务），或数据源不能计算——零数据标的（“本地数据”）、
// 窗口内存超过预算与变更日志断档（这两种检查项名都是“变更同步”）。
func (r *Runtime) taskError(set domain.EngineTask) error {
	if r.sources == nil {
		return nil
	}
	if err := r.sources.LookbackError(set); err != nil {
		return err
	}
	return r.sources.Blocked(seriesstore.Source{SpaceID: set.Set.SpaceID, DatasetID: set.Set.SourceDatasetID, Freq: set.Set.Freq})
}

// sourceGate 判断任务与数据源当前能不能计算。运行时由本地数据层（*localdata.Data）实现。
type sourceGate interface {
	// Blocked 返回数据源不能计算的原因：零数据标的、窗口内存超过预算、变更日志断档。
	Blocked(seriesstore.Source) error
	// LookbackError 返回任务里因子回看超过数据源回看数量 N 的错误。
	LookbackError(domain.EngineTask) error
}

// failsBeyondWindow 报告检查结果里除“本地窗口”“自定义算子模块”外还有没有不通过的项。这两项可能只是暂时不通过：
// 窗口载入由追赶的同步完成；模块是不是真的失败看 modulesFailed。
func failsBeyondWindow(checks []domain.TaskCheck) bool {
	for _, check := range checks {
		if check.Status == domain.TaskCheckFail && check.Name != windowCheckName && check.Name != modulesCheckName {
			return true
		}
	}
	return false
}

// modulesFailed 报告任务引用的自定义算子模块里有没有拉取失败或编译失败的：要人工处理，任务不参与追赶。
// 还没开始拉取、正在拉取的不算失败，任务照常参与追赶，等模块就绪（catchupModulesPending 报阻塞原因）。
func (r *Runtime) modulesFailed(set domain.EngineTask) bool {
	if r.udf == nil {
		return false
	}
	refs := refsOfTasks([]domain.EngineTask{set})
	if len(refs) == 0 {
		return false
	}
	states := map[string]string{}
	for _, module := range r.udf.Status() {
		states[module.Hash] = module.State
	}
	for _, ref := range refs {
		if r.udf.Ready(ref.hash) {
			continue
		}
		if state := states[ref.hash]; state == domain.UDFModuleFetchFailed || state == domain.UDFModuleCompileFailed {
			return true
		}
	}
	return false
}

// writeGate 是后台写结果路径（补算作业、缺口恢复、失效重算）的门禁：实时计算或恢复追赶允许时才写。
func (r *Runtime) writeGate(taskID string) bool { return r.gate(taskID) || r.catchupGate(taskID) }

// beginWrite 是写结果操作的登记式门禁：先登记在途，再检查门禁，不允许就立刻注销并拒绝。live 表示实时门禁放行，
// catchup 表示恢复追赶的门禁放行。暂停先关门禁、再等登记的在途操作结束，所以暂停收敛之后不会再有结果写出。
func (r *Runtime) beginWrite(_ context.Context, taskID string, live, catchup bool) (func(), bool) {
	release := r.writes.enter(taskID)
	if allowed := (live && r.gate(taskID)) || (catchup && r.catchupGate(taskID)); !allowed {
		release()
		return nil, false
	}
	return release, true
}

// quiesce 是生命周期的 Quiesce 钩子：门禁已经关闭，等在途的写结果操作（实时计算、补算分块、追赶推进、补算领取）结束。
func (r *Runtime) quiesce(ctx context.Context) error { return r.writes.wait(ctx, "") }

// leaseAllowed 报告是否可以领取补算作业：模块在运行、因子目录已载入才领取。不用 active()，它还服务于消费者、预载与数据同步。
func (r *Runtime) leaseAllowed() bool {
	return (r.life == nil || r.life.Running()) && r.catalog.Loaded()
}

// moduleRunning 报告模块是否在运行（补算等待门禁时据此区分“任务刚建立”与“模块暂停了”）。
func (r *Runtime) moduleRunning() bool { return r.life == nil || r.life.Running() }

// preloadBlocked 是预载循环的门禁：模块暂停、恢复追赶期间预载循环不把数据拉回本地。
func (r *Runtime) preloadBlocked() bool { return r.life != nil && r.life.Halted() }

// newGatedPreloader 建预载器并接上生命周期门禁：模块暂停、恢复追赶期间预载循环不运行，不会自己把数据拉回本地。
// 任务预载完成时立即重查这个任务，载入后马上开始计算，不用等下一轮检查。Initialize 与测试共用这一个构造函数。
func (r *Runtime) newGatedPreloader(source SourceDataset, data preloadData, onReady func(string)) *preloader {
	p := newPreloader(r.catalog, source, data, r.active, func(taskID string) {
		if onReady != nil {
			onReady(taskID)
		}
		r.recheckTask(taskID)
	})
	p.blocked = r.preloadBlocked
	return p
}

// refreshTaskChecks 检查目录里的全部任务并缓存结果；有任务的窗口还没载入时唤醒预载器（预载器本来就覆盖目录里的全部任务）。
func (r *Runtime) refreshTaskChecks(ctx context.Context) {
	ids := r.catalog.TaskIDs()
	r.checks.keep(ids)
	missing := false
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		r.checks.put(id, r.checkTask(ctx, id))
		missing = missing || !r.preload.Ready(id)
	}
	if missing {
		r.preload.Kick()
	}
}

// recheckTask 立即重查一个任务（预载完成时）。
func (r *Runtime) recheckTask(taskID string) {
	ctx := r.appCtx
	if ctx == nil {
		ctx = context.Background()
	}
	r.checks.put(taskID, r.checkTask(ctx, taskID))
}

// runTaskChecks 启动时检查一次，之后每隔 interval 重查。
func (r *Runtime) runTaskChecks(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		r.refreshTaskChecks(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// checkTask 检查一个因子任务能不能计算：有启用的成员、窗口已载入、本地数据覆盖回看、数据源没有变更日志断档、
// 回看数量够、引用的自定义算子模块就绪；长缺口只警告。
func (r *Runtime) checkTask(ctx context.Context, taskID string) []domain.TaskCheck {
	set, lookup := r.catalog.SetByID(taskID)
	if lookup != SetFound {
		return []domain.TaskCheck{{Name: "因子目录", Status: domain.TaskCheckFail, Message: "任务已不在目录里"}}
	}
	var checks []domain.TaskCheck
	if len(set.Members) == 0 {
		// 没有启用成员的任务不会产出任何列，不计算；成员启用、目录同步之后下一轮重查就会通过。
		checks = append(checks, domain.TaskCheck{Name: "启用成员", Status: domain.TaskCheckFail, Message: "任务没有启用的因子成员", Remedy: "启用至少一个成员：moox-cli factor member enable"})
	}
	ready := r.preload.Ready(taskID)
	if ready {
		checks = append(checks, domain.TaskCheck{Name: windowCheckName, Status: domain.TaskCheckPass})
	} else {
		checks = append(checks, domain.TaskCheck{Name: windowCheckName, Status: domain.TaskCheckFail, Message: "窗口还没有载入完成", Remedy: "引擎会自动载入，载入完成后立即开始计算；长时间不完成时看引擎日志的 factor_preload_failed"})
	}
	src := seriesstore.Source{SpaceID: set.Set.SpaceID, DatasetID: set.Set.SourceDatasetID, Freq: set.Set.Freq}
	if ready {
		checks = append(checks, r.coverageCheck(ctx, src))
	}
	checks = append(checks, r.blockedCheck(set, src))
	if check, ok := r.modulesCheck(set); ok {
		checks = append(checks, check)
	}
	if gaps := r.data.LongGaps(src); len(gaps) > 0 {
		first := gaps[0]
		checks = append(checks, domain.TaskCheck{
			Name: "长缺口", Status: domain.TaskCheckWarn,
			Message: gapMessage(len(gaps), first),
			Remedy:  "确认交易所是否仍有该标的的行情；不需要时把它从源数据集的标签里移除，或在因子里用 is_filled 排除填充值",
		})
	}
	return checks
}

func gapMessage(count int, first localdata.LongGap) string {
	if first.Stale {
		return fmt.Sprintf("%d 个标的长时间没有新数据（例如 %s 已 %s 没有新行情）", count, first.Subject, first.Duration.Round(time.Minute))
	}
	return fmt.Sprintf("%d 个标的的窗口里有长缺口（例如 %s 连续填充 %d 根、约 %s），填充值仍参与计算", count, first.Subject, first.Rows, first.Duration.Round(time.Minute))
}

// blockedCheck 把任务当前不能计算的原因翻成检查项：零数据标的要导入或移除，因子回看超过 N 要调 N，变更日志断档要对账。
func (r *Runtime) blockedCheck(set domain.EngineTask, src seriesstore.Source) domain.TaskCheck {
	taskID := set.Set.TaskID
	err := r.taskError(set)
	if err == nil {
		return domain.TaskCheck{Name: "变更同步", Status: domain.TaskCheckPass}
	}
	var noData *localdata.NoDataError
	var over *localdata.LookbackExceededError
	switch {
	case errors.As(err, &noData):
		return domain.TaskCheck{
			Name: "本地数据", Status: domain.TaskCheckFail, Message: err.Error(),
			Remedy: fmt.Sprintf("导入历史数据：moox-cli import --target factor --space-id %s --dataset-id %s --frequency %s --path <历史文件>；或把这些标的从源数据集的标签里移除", src.SpaceID, src.DatasetID, src.Freq),
		}
	case errors.As(err, &over):
		return domain.TaskCheck{
			Name: "回看数量", Status: domain.TaskCheckFail, Message: err.Error(),
			Remedy: fmt.Sprintf("在 engine.yaml 里调大 window.lookback（7×24 市场）或 window.session_lookback（有交易时段的市场），或设置 window.lookback_overrides 的 %s/%s/%s，重启引擎", src.SpaceID, src.DatasetID, src.Freq),
		}
	}
	return domain.TaskCheck{
		Name: "变更同步", Status: domain.TaskCheckFail, Message: err.Error(),
		Remedy: fmt.Sprintf("先停止引擎，再执行 moox-factor-engine reconcile --task %s", taskID),
	}
}

// coverageCheck 检查每个标的的本地连续数据满足回看要求，或属于新标的（本地最早一根就是 Storage 的首根）；不满足的列出来并提示导入。
func (r *Runtime) coverageCheck(ctx context.Context, src seriesstore.Source) domain.TaskCheck {
	short, err := r.data.Coverage(ctx, src)
	if err != nil {
		return domain.TaskCheck{Name: "本地数据覆盖", Status: domain.TaskCheckWarn, Message: "无法核对：" + err.Error()}
	}
	if len(short) == 0 {
		return domain.TaskCheck{Name: "本地数据覆盖", Status: domain.TaskCheckPass}
	}
	first := short[0]
	return domain.TaskCheck{
		Name: "本地数据覆盖", Status: domain.TaskCheckFail,
		Message: fmt.Sprintf("%d 个标的的本地数据不足回看根数，例如 %s：本地 %d 根（最早 %s），需要 %d 根",
			len(short), first.Subject, first.Have, src.Format(first.Earliest), first.Want),
		Remedy: fmt.Sprintf("导入更早的历史：moox-cli import --target factor --space-id %s --dataset-id %s --frequency %s --path <历史文件>", src.SpaceID, src.DatasetID, src.Freq),
	}
}

// lifecycleStatus 是随心跳上报的模块级生命周期状态：管理端据此确认本次暂停已收敛（切换自定义算子版本的前提），
// 并把暂停类型、原因与追赶进度代报给 Monitor（首页的系统状态读它）。
func (r *Runtime) lifecycleStatus() domain.EngineLifecycle {
	if r.life == nil {
		return domain.EngineLifecycle{}
	}
	state := r.life.State()
	out := domain.EngineLifecycle{Stage: string(state.Stage), QuiescedAt: state.QuiescedAt, Quiesced: state.Quiesced(), CatchupBlocked: r.catchupPreconditionFor(r.catchupMembers()), Catchup: state.Catchup}
	if state.Pause != nil {
		out.PausedSince, out.PauseKind, out.PauseReason = state.Pause.Since, string(state.Pause.Kind), state.Pause.Reason
	}
	return out
}

// markRunners 给心跳里的任务补上暂停标记与检查结果。暂停标记只看模块是否被有意停下（暂停，以及放行前的恢复追赶）：
// 管理端据此不再期望结果数据集持续产出，Monitor 也就不对它做新鲜度检查。检查没通过的任务不算暂停，照常做新鲜度检查。
func (r *Runtime) markRunners(runners []domain.RunnerStatus) {
	halted := r.life != nil && r.life.Halted()
	for i := range runners {
		runners[i].Paused = halted
		runners[i].Checks, _ = r.checks.get(runners[i].TaskID)
	}
}
