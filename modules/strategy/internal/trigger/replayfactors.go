package trigger

import (
	"context"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
)

// resolveForReplay 解析回放要用的绑定：按实例关联的因子任务，用当前的 DSL 在任务的结果 View 上编译。
// 回放只读，不关联任务、不改动实例；任务的列与可变成员都以当前元数据为准。
func (e *ReplayEvaluator) resolveForReplay(ctx context.Context, instance store.Instance, strategy dsl.Strategy) (input.Resolved, *dsl.Program, error) {
	return input.Resolve(ctx, e.Client, instance.SpaceID, instance.FactorTaskID, strategy)
}
