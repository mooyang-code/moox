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
			Reset:       r.resetSpec(),
			OnChange: func(ref lifecycle.Ref, from, to lifecycle.Stage, reason string) {
				log.Infof("factor_lifecycle_changed kind=%s id=%s from=%s to=%s reason=%s", ref.Kind, ref.ID, from, to, reason)
			},
		},
	})
}

// gate 是实时周期处理前的门禁：任务没有处于预热或运行就不计算。
func (r *Runtime) gate(taskID string) bool { return r.life.Allowed(setRef(taskID)) }

// catchupGate 是恢复追赶的门禁：只写历史数据（连续水位追赶、历史周期补算）；暂停、重置期间一律关闭。
func (r *Runtime) catchupGate(taskID string) bool { return r.life.CatchupAllowed(setRef(taskID)) }

// writeGate 是后台写结果路径（补算作业、缺口恢复、失效重算）的门禁：实时计算或恢复追赶允许时才写。
// 这几条路径原先只看目录与租约（active），任务暂停后仍会写结果，现在统一并入生命周期门禁。
func (r *Runtime) writeGate(taskID string) bool { return r.gate(taskID) || r.catchupGate(taskID) }

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
// 建好就该直接开始预热计算（策略实例的私有因子任务在实例启用时建立，同样如此）。
func (r *Runtime) autoStartTask(ref lifecycle.Ref) bool {
	if ref.Kind != factorTaskKind {
		return false
	}
	_, lookup := r.catalog.SetByID(ref.ID)
	return lookup == SetFound
}

// autoConfirmTask 报告预热中的计算任务能否自动确认进入运行：只对还没关联策略实例的任务（已关联任务的预热阶段有“策略对预热实例
// 发影子目标”的语义，不自动确认）。证据只放在引擎内存里：任务进入预热之后，有一期所有启用成员都完整算完（没有失败、没有预热中的标的）、
// 写入了结果行，且这一期各成员的成员契约（成员键、类型、定义哈希）都与当前目录一致。成员变化、再次进入预热、引擎重启都会使证据作废，
// 代价至多是晚一个周期转为运行；因子计算在预热阶段本来就是完整的。
func (r *Runtime) autoConfirmTask(ref lifecycle.Ref, since time.Time) bool {
	if ref.Kind != factorTaskKind {
		return false
	}
	set, lookup := r.catalog.SetByID(ref.ID)
	if lookup != SetFound || set.Set.BoundInstanceID != "" {
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
