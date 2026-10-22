package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	factorhealth "github.com/mooyang-code/moox/modules/factor/internal/health"
	"github.com/mooyang-code/moox/modules/factor/internal/localdata"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"trpc.group/trpc-go/trpc-go/log"
)

// 引擎的生命周期（设计第 9 节）：模块自身是基础设施，对象是目录里启用的因子集。
// 只有处于预热或运行的集合才计算；检查不过的集合自动暂停，检查恢复后自动回到原阶段，其他集合不受影响。

const factorSetKind = "factor_set"

func setRef(setID string) lifecycle.Ref { return lifecycle.Ref{Kind: factorSetKind, ID: setID} }

// newLifecycle 创建引擎的生命周期控制器。
func (r *Runtime) newLifecycle() (*lifecycle.Controller, error) {
	return lifecycle.New(lifecycle.Options{
		Module:    "factor-engine",
		StateFile: r.cfg.Lifecycle.StateFile,
		Hooks: lifecycle.Hooks{
			CheckModule: r.checkModule,
			CheckObject: r.checkSet,
			Objects:     r.lifecycleObjects,
			Init:        r.initSet,
			OnChange: func(ref lifecycle.Ref, from, to lifecycle.Stage, reason string) {
				log.Infof("factor_lifecycle_changed kind=%s id=%s from=%s to=%s reason=%s", ref.Kind, ref.ID, from, to, reason)
			},
		},
	})
}

// gate 是周期处理前的门禁：集合没有处于预热或运行就不计算。
func (r *Runtime) gate(setID string) bool { return r.life.Allowed(setRef(setID)) }

func (r *Runtime) lifecycleObjects() []lifecycle.Ref {
	ids := r.catalog.SetIDs()
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

// checkSet 检查一个因子集能不能计算：窗口已就绪、数据源没有变更日志断档。
func (r *Runtime) checkSet(ctx context.Context, ref lifecycle.Ref) []lifecycle.Check {
	set, lookup := r.catalog.SetByID(ref.ID)
	if lookup != SetFound {
		return []lifecycle.Check{{Name: "因子目录", Status: lifecycle.CheckFail, Message: "集合已不在目录里"}}
	}
	var checks []lifecycle.Check
	if r.preload.Ready(ref.ID) {
		checks = append(checks, lifecycle.Check{Name: "本地窗口", Status: lifecycle.CheckPass})
	} else {
		checks = append(checks, lifecycle.Check{Name: "本地窗口", Status: lifecycle.CheckFail, Message: "窗口还没有载入完成", Remedy: "moox-cli lifecycle init --set " + ref.ID})
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
			Remedy:  "确认交易所是否仍有该标的的行情；不需要时把它从集合的标的列表里移除，或在因子里用 is_filled 排除填充值",
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
func (r *Runtime) blockedCheck(src seriesstore.Source, setID string) lifecycle.Check {
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
			Remedy: fmt.Sprintf("导入历史数据：moox-cli import --target factor --space %s --dataset %s --freq %s --path <历史文件>；或把这些标的从集合的标的列表里移除", src.SpaceID, src.DatasetID, src.Freq),
		}
	case errors.As(err, &over):
		return lifecycle.Check{
			Name: "回看数量", Status: lifecycle.CheckFail, Message: err.Error(),
			Remedy: fmt.Sprintf("在 engine.yaml 里调大 window.lookback（或 window.lookback_overrides 的 %s/%s/%s），重启引擎", src.SpaceID, src.DatasetID, src.Freq),
		}
	}
	return lifecycle.Check{
		Name: "变更同步", Status: lifecycle.CheckFail, Message: err.Error(),
		Remedy: fmt.Sprintf("先停止引擎，再执行 moox-factor-engine reconcile --set %s", setID),
	}
}

// initSet 为集合准备本地数据（读字段、取同步基线、补到最新已收盘的一根、载入窗口）；模块自身没有动作。
func (r *Runtime) initSet(ctx context.Context, ref lifecycle.Ref) error {
	if ref == (lifecycle.Ref{}) {
		return nil
	}
	return r.preload.PrepareSet(ctx, ref.ID)
}

// lifecycleRoutes 是挂在健康端口上的生命周期接口（状态与命令）。
func (r *Runtime) lifecycleRoutes() []factorhealth.Route {
	return []factorhealth.Route{
		{Path: lifecycle.StatusPath, Handler: r.life.StatusHandler()},
		{Path: lifecycle.CommandPath, Handler: r.life.CommandHandler()},
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
