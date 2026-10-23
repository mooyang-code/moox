package rpc

import (
	"context"
	"errors"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"trpc.group/trpc-go/trpc-go/log"
)

// resolveForInstance 解析实例的绑定：声明了 factors 的策略先为实例建立私有因子集再绑定它的结果 View，其余直接解析实例的 View。
func (s *Service) resolveForInstance(ctx context.Context, instance store.Instance, strategy dsl.Strategy) (input.Resolved, *dsl.Program, error) {
	if len(strategy.Factors) == 0 {
		return s.Resolver.Resolve(ctx, instance.SpaceID, instance.ViewID, strategy)
	}
	resolver, ok := s.Resolver.(InstanceResolver)
	if !ok {
		return input.Resolved{}, nil, errors.New("Factor 依赖未配置，不能启用声明了 factors 的策略")
	}
	return resolver.ResolveInstance(ctx, instance.InstanceID, instance.SpaceID, instance.ViewID, strategy)
}

// releaseFactorSet 删除实例的私有因子集（幂等）；没有接线 Factor 时无事可做。
func (s *Service) releaseFactorSet(ctx context.Context, instanceID string) error {
	if s.Factors == nil {
		return nil
	}
	return s.Factors.ReleaseInstanceFactorSet(ctx, instanceID)
}

// pauseFactorSet 停用实例的私有因子集：尽力而为，失败不阻止实例停用（再次启用时会重新对齐并启用）。
func (s *Service) pauseFactorSet(ctx context.Context, instanceID string) {
	if s.Factors == nil {
		return
	}
	if err := s.Factors.SetInstanceFactorSetEnabled(ctx, instanceID, false); err != nil {
		log.Warnf("strategy_factor_set_pause_failed instance_id=%s error=%v", instanceID, err)
	}
}
