package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"trpc.group/trpc-go/trpc-go/log"
)

// resolveForInstance 解析实例的绑定：声明了 factors 的策略先为实例建立私有因子任务再绑定它的结果 View，其余直接解析实例的 View。
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

// releaseFactorTask 删除实例的私有因子任务（幂等）；没有接线 Factor 时无事可做。其他未删除的实例还绑定着它的结果 View 时
// 拒绝：删除私有因子任务会连同默认结果 View 一起删掉，读取方就会永久失去 View 与历史结果。
func (s *Service) releaseFactorTask(ctx context.Context, instance store.Instance) error {
	if s.Factors == nil {
		return nil
	}
	if err := s.ensureNoViewReaders(ctx, instance); err != nil {
		return err
	}
	return s.Factors.ReleaseInstanceFactorTask(ctx, instance.InstanceID)
}

// ensureNoViewReaders 检查是否还有别的实例绑定了 owner 的私有结果 View（取自 owner 固化的解析结果）。
func (s *Service) ensureNoViewReaders(ctx context.Context, owner store.Instance) error {
	var resolved struct {
		ViewID       string `json:"view_id"`
		FactorTaskID string `json:"factor_task_id"`
	}
	if len(owner.ResolvedJSON) == 0 || json.Unmarshal(owner.ResolvedJSON, &resolved) != nil || resolved.FactorTaskID == "" || resolved.ViewID == "" {
		return nil
	}
	instances, err := s.Store.ListInstances(ctx, owner.SpaceID, nil)
	if err != nil {
		return err
	}
	for _, other := range instances {
		if other.InstanceID != owner.InstanceID && other.ViewID == resolved.ViewID {
			return fmt.Errorf("实例 %s 还绑定着私有结果 View %s，请先改绑或删除它", other.InstanceID, resolved.ViewID)
		}
	}
	return nil
}

// pauseFactorTask 停用实例的私有因子任务：尽力而为，失败不阻止实例停用（再次启用时会重新对齐并启用）。
func (s *Service) pauseFactorTask(ctx context.Context, instanceID string) {
	if s.Factors == nil {
		return
	}
	if err := s.Factors.SetInstanceFactorTaskEnabled(ctx, instanceID, false); err != nil {
		log.Warnf("strategy_factor_task_pause_failed instance_id=%s error=%v", instanceID, err)
	}
}
