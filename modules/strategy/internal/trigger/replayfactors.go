package trigger

import (
	"context"
	"errors"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
)

// resolveForReplay 解析回放要用的绑定。没有声明 factors 的策略直接解析实例绑定的 View；声明了 factors 的策略读的是
// 实例私有因子任务的结果 View：取实例启用时固化的解析（私有 View、私有因子任务、各成员的参数声明），再按当前 DSL 在那个 View
// 上编译。回放不新建、不改动实例的私有因子任务，所以实例必须启用过（至少有一次固化的解析）。
func (e *ReplayEvaluator) resolveForReplay(ctx context.Context, instance store.Instance, strategy dsl.Strategy) (input.Resolved, *dsl.Program, error) {
	if len(strategy.Factors) == 0 {
		return input.Resolve(ctx, e.Client, instance.SpaceID, instance.ViewID, strategy)
	}
	bound, err := input.ParseResolved(instance.ResolvedJSON)
	if err != nil || bound.FactorTaskID == "" {
		return input.Resolved{}, nil, errors.New("声明了 factors 的策略要先启用实例（建立私有因子任务）才能回放")
	}
	resolved, program, err := input.Resolve(ctx, e.Client, instance.SpaceID, bound.ViewID, strategy)
	if err != nil {
		return input.Resolved{}, nil, err
	}
	resolved.SourceViewID, resolved.FactorTaskID, resolved.FactorParams = bound.SourceViewID, bound.FactorTaskID, bound.FactorParams
	return resolved, program, nil
}
