package rpc

import (
	"context"
	"fmt"
)

// rejectOwnedSet 拒绝手工修改策略实例的私有因子集：它的成员恰好是策略 factors 声明的因子、状态随实例启停，
// 由实例启用、停用与删除时自动维护（EnsureInstanceFactorSet 等）；手工改动会被下次启用覆盖，或让实例读到不一致的因子。
// 集合不存在或读取失败时放行，由后面的操作自己报错。
func (s *Service) rejectOwnedSet(ctx context.Context, setID string) error {
	set, _, err := s.catalog.GetSet(ctx, setID)
	if err != nil || set.OwnerInstanceID == "" {
		return nil
	}
	return fmt.Errorf("因子集 %s 是策略实例 %s 的私有因子集，成员与状态由实例维护，不能手工修改", setID, set.OwnerInstanceID)
}
