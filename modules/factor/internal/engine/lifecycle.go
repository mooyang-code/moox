package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	factorhealth "github.com/mooyang-code/moox/modules/factor/internal/health"
	"github.com/mooyang-code/moox/modules/factor/internal/localdata"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"trpc.group/trpc-go/trpc-go/log"
)

// 引擎的生命周期（设计第 9 节）：模块自身是基础设施，对象是目录里启用的因子任务。
// 只有处于预热或运行的任务才计算；检查不过的任务自动暂停，检查恢复后自动回到原阶段，其他任务不受影响。

const factorTaskKind = "factor_task"

func setRef(taskID string) lifecycle.Ref { return lifecycle.Ref{Kind: factorTaskKind, ID: taskID} }

// newLifecycle 创建引擎的生命周期控制器。
func (r *Runtime) newLifecycle() (*lifecycle.Controller, error) {
	return lifecycle.New(lifecycle.Options{
		Audit:     func(event string) { log.Infof("%s", event) },
		Module:    "factor-engine",
		StateFile: r.cfg.Lifecycle.StateFile,
		Hooks: lifecycle.Hooks{
			CheckModule: r.checkModule,
			CheckObject: r.checkTask,
			Objects:     r.lifecycleObjects,
			Init:        r.initTask,
			AutoStart:   r.autoStartTask,
			AutoConfirm: r.autoConfirmTask,
			Catchup:     r.catchupHook,
			Quiesce:     r.quiesce,
			Reset:       r.resetSpec(),
			OnChange: func(ref lifecycle.Ref, from, to lifecycle.Stage, reason string) {
				if to == lifecycle.StageResuming {
					// 恢复一开始就同步目录：追赶的前置条件要等本次恢复之后的同步。
					r.syncer.SyncSoon()
				}
				log.Infof("factor_lifecycle_changed kind=%s id=%s from=%s to=%s reason=%s", ref.Kind, ref.ID, from, to, reason)
			},
		},
	})
}

// gate 是实时周期处理前的门禁：任务没有处于预热或运行就不计算。
func (r *Runtime) gate(taskID string) bool { return r.life.Allowed(setRef(taskID)) }

// catchupGate 是恢复追赶的门禁：只写历史数据（连续水位追赶、历史周期补算）；暂停、重置期间一律关闭。
// 除生命周期允许外还要满足追赶前置条件：本次恢复之后至少成功同步过一次目录（见 catchupPrecondition），
// 否则可能先用暂停前的旧目录写出结果。
func (r *Runtime) catchupGate(taskID string) bool {
	return r.life.CatchupAllowed(setRef(taskID)) && r.catchupPrecondition(taskID) == ""
}

// resumeStartedAt 是这个任务本次恢复追赶开始的时间：模块在恢复追赶就取模块的，否则取任务自己的；不在恢复追赶时为零。
func (r *Runtime) resumeStartedAt(taskID string) time.Time {
	if r.life.Stage(lifecycle.Ref{}) == lifecycle.StageResuming {
		return r.life.StageSince(lifecycle.Ref{})
	}
	if r.life.Stage(setRef(taskID)) == lifecycle.StageResuming {
		return r.life.StageSince(setRef(taskID))
	}
	return time.Time{}
}

// catchupPrecondition 返回追赶前置条件未满足的原因，满足时为空。追赶在写任何结果（推进水位、后台补算）之前都要满足：
// 本次恢复开始之后目录至少成功同步过一次（返回已应用或未变化都算）。没有目录同步器的运行时（诊断入口、测试）不要求。
func (r *Runtime) catchupPrecondition(taskID string) string {
	started := r.resumeStartedAt(taskID)
	if started.IsZero() {
		return ""
	}
	// 追赶写结果之前，目录里引用的自定义算子模块必须全部就绪：不会拿旧版本、或还没编译好的版本计算。
	if reason := r.catchupModulesPending(taskID); reason != "" {
		return reason
	}
	if r.syncer == nil {
		return ""
	}
	if !r.catalog.Status().SyncedAt.Before(started) {
		return ""
	}
	r.syncer.SyncSoon()
	return "等待本次恢复之后的目录同步（管理端不可达时会一直等待）"
}

// catchupModulesPending 返回恢复追赶时引用的自定义算子模块还没有全部就绪的原因，就绪或没有引用时为空。
// taskID 为空时检查目录里全部任务（模块级恢复）。
func (r *Runtime) catchupModulesPending(taskID string) string {
	if r.udf == nil || r.catalog == nil {
		return ""
	}
	var tasks []domain.EngineTask
	if taskID == "" {
		tasks = r.catalog.Sets()
	} else if set, lookup := r.catalog.SetByID(taskID); lookup == SetFound {
		tasks = []domain.EngineTask{set}
	}
	for _, ref := range refsOfTasks(tasks) {
		if !r.udf.Ready(ref.hash) {
			return fmt.Sprintf("自定义算子 %s@%.12s 的模块还没有就绪（拉取或编译中、或失败，见引擎状态的模块列表）", ref.name, ref.hash)
		}
	}
	return ""
}

// writeGate 是后台写结果路径（补算作业、缺口恢复、失效重算）的门禁：实时计算或恢复追赶允许时才写。
// 这几条路径原先只看目录与租约（active），任务暂停后仍会写结果，现在统一并入生命周期门禁。
func (r *Runtime) writeGate(taskID string) bool { return r.gate(taskID) || r.catchupGate(taskID) }

// leaseGenerationKey 是补算作业领取时记下的模块暂停代次，放在执行上下文里。
type leaseGenerationKey struct{}

// withLeaseGeneration 返回带着暂停代次的上下文：代次之后发生过暂停，这个上下文里的写结果操作一律被拒绝。
func withLeaseGeneration(ctx context.Context, generation uint64) context.Context {
	return context.WithValue(ctx, leaseGenerationKey{}, generation)
}

// beginWrite 是写结果操作的登记式门禁：先登记在途，再检查门禁，不允许就立刻注销并拒绝。live 表示实时门禁放行，
// catchup 表示恢复追赶的门禁放行；上下文带着领取代次时，代次变化（期间暂停过）也拒绝。
func (r *Runtime) beginWrite(ctx context.Context, taskID string, live, catchup bool) (func(), bool) {
	release := r.writes.enter(taskID)
	allowed := (live && r.gate(taskID)) || (catchup && r.catchupGate(taskID))
	if allowed {
		if generation, ok := ctx.Value(leaseGenerationKey{}).(uint64); ok && generation != r.life.ModuleGeneration() {
			allowed = false
		}
	}
	if !allowed {
		release()
		return nil, false
	}
	return release, true
}

// beginReplayWrite 是回放写回的登记式门禁：回放的任务不一定处于预热或运行，所以只在被有意停下（暂停、恢复追赶、重置、故障）时拒绝。
func (r *Runtime) beginReplayWrite(_ context.Context, taskID string) (func(), bool) {
	release := r.writes.enter(taskID)
	if r.life.Halted(lifecycle.Ref{}) || r.life.Halted(setRef(taskID)) {
		release()
		return nil, false
	}
	return release, true
}

// quiesce 是生命周期的 Quiesce 钩子：门禁已经关闭，等在途的写结果操作（实时计算、补算分块、追赶推进、回放写回、补算领取）结束。
func (r *Runtime) quiesce(ctx context.Context, ref lifecycle.Ref) error {
	taskID := ""
	if ref != (lifecycle.Ref{}) {
		taskID = ref.ID
	}
	return r.writes.wait(ctx, taskID)
}

// leaseAllowed 报告是否可以领取补算作业：模块级生命周期放行才领取。不用 active()，它还服务于消费者、预载与数据同步。
func (r *Runtime) leaseAllowed() bool { return r.life == nil || r.life.ModuleOpen() }

func (r *Runtime) lifecycleObjects() []lifecycle.Ref {
	ids := r.catalog.TaskIDs()
	refs := make([]lifecycle.Ref, 0, len(ids))
	for _, id := range ids {
		refs = append(refs, setRef(id))
	}
	return refs
}

func (r *Runtime) checkModule(context.Context) []lifecycle.Check {
	checks := []lifecycle.Check{{Name: "本地时序存储", Status: lifecycle.CheckPass}}
	if r.catalog.Loaded() {
		checks = append(checks, lifecycle.Check{Name: "因子目录", Status: lifecycle.CheckPass})
	} else {
		checks = append(checks, lifecycle.Check{Name: "因子目录", Status: lifecycle.CheckFail, Message: "尚未同步到因子目录"})
	}
	if r.lease.Conflict() {
		checks = append(checks, lifecycle.Check{Name: "租约", Status: lifecycle.CheckFail, Message: "另一个引擎持有租约"})
	}
	return checks
}

// checkTask 检查一个因子任务能不能计算：窗口已就绪、数据源没有变更日志断档。
func (r *Runtime) checkTask(ctx context.Context, ref lifecycle.Ref) []lifecycle.Check {
	set, lookup := r.catalog.SetByID(ref.ID)
	if lookup != SetFound {
		return []lifecycle.Check{{Name: "因子目录", Status: lifecycle.CheckFail, Message: "任务已不在目录里"}}
	}
	var checks []lifecycle.Check
	if r.preload.Ready(ref.ID) {
		checks = append(checks, lifecycle.Check{Name: "本地窗口", Status: lifecycle.CheckPass})
	} else {
		checks = append(checks, lifecycle.Check{Name: "本地窗口", Status: lifecycle.CheckFail, Message: "窗口还没有载入完成", Remedy: "moox-cli lifecycle init --task " + ref.ID})
	}
	src := seriesstore.Source{SpaceID: set.Set.SpaceID, DatasetID: set.Set.SourceDatasetID, Freq: set.Set.Freq}
	if r.preload.Ready(ref.ID) {
		checks = append(checks, r.coverageCheck(ctx, src))
	}
	checks = append(checks, r.blockedCheck(src, ref.ID))
	if check, ok := r.modulesCheck(set); ok {
		checks = append(checks, check)
	}
	if gaps := r.data.LongGaps(src); len(gaps) > 0 {
		first := gaps[0]
		checks = append(checks, lifecycle.Check{
			Name: "长缺口", Status: lifecycle.CheckWarn,
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

// blockedCheck 把数据源当前不能计算的原因翻成检查项：零数据标的要导入或移除，因子回看超过 N 要调 N，变更日志断档要对账。
func (r *Runtime) blockedCheck(src seriesstore.Source, taskID string) lifecycle.Check {
	err := r.data.Blocked(src)
	if err == nil {
		return lifecycle.Check{Name: "变更同步", Status: lifecycle.CheckPass}
	}
	var noData *localdata.NoDataError
	var over *localdata.LookbackExceededError
	switch {
	case errors.As(err, &noData):
		return lifecycle.Check{
			Name: "本地数据", Status: lifecycle.CheckFail, Message: err.Error(),
			Remedy: fmt.Sprintf("导入历史数据：moox-cli import --target factor --space %s --dataset %s --freq %s --path <历史文件>；或把这些标的从源数据集的标签里移除", src.SpaceID, src.DatasetID, src.Freq),
		}
	case errors.As(err, &over):
		return lifecycle.Check{
			Name: "回看数量", Status: lifecycle.CheckFail, Message: err.Error(),
			Remedy: fmt.Sprintf("在 engine.yaml 里调大 window.lookback（或 window.lookback_overrides 的 %s/%s/%s），重启引擎", src.SpaceID, src.DatasetID, src.Freq),
		}
	}
	return lifecycle.Check{
		Name: "变更同步", Status: lifecycle.CheckFail, Message: err.Error(),
		Remedy: fmt.Sprintf("先停止引擎，再执行 moox-factor-engine reconcile --task %s", taskID),
	}
}

// initTask 为任务准备本地数据（读字段、取同步基线、补到最新已收盘的一根、载入窗口）；模块自身没有动作。
func (r *Runtime) initTask(ctx context.Context, ref lifecycle.Ref) error {
	if ref == (lifecycle.Ref{}) {
		return nil
	}
	return r.preload.PrepareSet(ctx, ref.ID)
}

// autoStartTask 报告任务是否随创建自动 init、start：目录里的计算任务没有人会逐个执行生命周期命令，
// 有启用成员的任务建好就该直接开始预热计算。没有启用成员的任务不启动（它不会产出任何列，启动没有意义；
// 周期刷新会在成员启用、目录同步之后重新判断）。本地序列是否覆盖成员里最长的“回看 + 预热”由预热阶段的数据检查负责，
// 可变成员的回看按参数声明范围的上限计算（编译时已取最坏情况），不够时停在预热阶段，状态里给出需要导入历史的提示。
func (r *Runtime) autoStartTask(ref lifecycle.Ref) bool {
	if ref.Kind != factorTaskKind {
		return false
	}
	set, lookup := r.catalog.SetByID(ref.ID)
	return lookup == SetFound && len(set.Members) > 0
}

// autoConfirmTask 报告试运行中的计算任务能否自动确认进入运行（任务与策略实例相互独立，是否关联实例不影响）。
// 证据只放在引擎内存里：任务进入试运行之后，有一期所有启用成员都完整算完（没有失败、没有预热中的标的）、
// 写入了结果行，且这一期各成员的成员契约（成员键、类型、定义哈希）都与当前目录一致。成员变化、再次进入预热、引擎重启都会使证据作废，
// 代价至多是晚一个周期转为运行；因子计算在预热阶段本来就是完整的。
func (r *Runtime) autoConfirmTask(ref lifecycle.Ref, since time.Time) bool {
	if ref.Kind != factorTaskKind {
		return false
	}
	set, lookup := r.catalog.SetByID(ref.ID)
	if lookup != SetFound {
		return false
	}
	run, ok := r.runs.lastRecorded(ref.ID)
	if !ok || !run.completedAt.After(since) {
		return false
	}
	return runConfirmsWarmup(run, set.Members)
}

// runConfirmsWarmup 报告一期的结果能否作为预热完成的证据。
func runConfirmsWarmup(run trackedRun, members []domain.MemberDef) bool {
	if run.status != "complete" || run.warming > 0 || len(run.failedSubjects) > 0 || run.rows == 0 || len(members) == 0 {
		return false
	}
	if len(run.factors) != len(members) {
		return false
	}
	want := make(map[string]domain.MemberDef, len(members))
	for _, member := range members {
		want[member.Key] = member
	}
	for _, factor := range run.factors {
		member, ok := want[factor.MemberKey]
		if !ok || factor.Status != "complete" || len(factor.FailedSubjects) > 0 || len(factor.GapSubjects) > 0 {
			return false
		}
		// 证据要绑定成员的契约：成员键已经带了固定参数，再核对类型与定义哈希，成员删除重加成不同的定义或类型后，
		// 旧的一期不能算新成员的证据。
		compiled, err := domain.CompileDefinition(member.Factor)
		if err != nil {
			return false
		}
		if factor.MemberKind != member.Kind || factor.DefinitionHash != domain.DefinitionHash(compiled) {
			return false
		}
		delete(want, factor.MemberKey)
	}
	return len(want) == 0
}

// lifecycleRoutes 是挂在健康端口上的生命周期接口（状态与命令）。
func (r *Runtime) lifecycleRoutes() []factorhealth.Route {
	return []factorhealth.Route{
		{Path: lifecycle.StatusPath, Handler: r.life.StatusHandler()},
		{Path: lifecycle.CommandPath, Handler: r.life.CommandHandler()},
		{Path: lifecycle.CatchupPath, Handler: r.life.CatchupHandler()},
		{Path: lifecycle.ResetPrefix, Handler: r.life.ResetHandler()},
	}
}

// coverageCheck 检查每个标的的本地连续数据满足回看要求，或属于新标的（本地最早一根就是 Storage 的首根）；不满足的列出来并提示导入。
func (r *Runtime) coverageCheck(ctx context.Context, src seriesstore.Source) lifecycle.Check {
	short, err := r.data.Coverage(ctx, src)
	if err != nil {
		return lifecycle.Check{Name: "本地数据覆盖", Status: lifecycle.CheckWarn, Message: "无法核对：" + err.Error()}
	}
	if len(short) == 0 {
		return lifecycle.Check{Name: "本地数据覆盖", Status: lifecycle.CheckPass}
	}
	first := short[0]
	return lifecycle.Check{
		Name: "本地数据覆盖", Status: lifecycle.CheckFail,
		Message: fmt.Sprintf("%d 个标的的本地数据不足回看根数，例如 %s：本地 %d 根（最早 %s），需要 %d 根",
			len(short), first.Subject, first.Have, first.Earliest.UTC().Format(time.RFC3339), first.Want),
		Remedy: fmt.Sprintf("导入更早的历史：moox-cli import --target factor --space %s --dataset %s --freq %s --path <历史文件>", src.SpaceID, src.DatasetID, src.Freq),
	}
}

// lifecycleStatus 是随心跳上报的模块级生命周期状态：管理端据此确认本次暂停已收敛（切换自定义算子版本的前提）。
func (r *Runtime) lifecycleStatus() domain.EngineLifecycle {
	if r.life == nil {
		return domain.EngineLifecycle{}
	}
	self := r.life.Snapshot().Self
	out := domain.EngineLifecycle{Stage: string(self.Stage), QuiescedAt: self.QuiescedAt, Quiesced: self.Quiesced(), CatchupBlocked: r.catchupPrecondition("")}
	if self.Pause != nil {
		out.PausedSince = self.Pause.Since
	}
	return out
}

// markPausedRunners 标出被有意停下的任务（手动、系统或自动暂停，以及放行前的恢复追赶；故障不算，要照常告警），
// 随心跳上报给管理端：管理端据此不再期望它的结果数据集持续产出，Monitor 也就不对它做新鲜度检查。
// 恢复追赶期间任务不计算实时周期，要等放行才开始产出；这段时间若恢复检查，暂停前留下的旧水位会立刻判成过期。
func markPausedRunners(runners []domain.RunnerStatus, life *lifecycle.Controller) {
	if life == nil {
		return
	}
	for i := range runners {
		stage := life.Stage(setRef(runners[i].TaskID))
		runners[i].Paused = stage == lifecycle.StagePaused || stage == lifecycle.StageResuming
	}
}
