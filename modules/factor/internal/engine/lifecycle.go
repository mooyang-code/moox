package engine

import (
	"context"
	"fmt"

	factorhealth "github.com/mooyang-code/moox/modules/factor/internal/health"
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
func (r *Runtime) checkSet(_ context.Context, ref lifecycle.Ref) []lifecycle.Check {
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
	if err := r.data.Blocked(src); err != nil {
		checks = append(checks, lifecycle.Check{
			Name: "变更同步", Status: lifecycle.CheckFail, Message: err.Error(),
			Remedy: fmt.Sprintf("先停止引擎，再执行 moox-factor-engine reconcile --set %s", ref.ID),
		})
	} else {
		checks = append(checks, lifecycle.Check{Name: "变更同步", Status: lifecycle.CheckPass})
	}
	return checks
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
