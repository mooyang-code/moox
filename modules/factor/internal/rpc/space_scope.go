package rpc

import (
	"context"
	"fmt"
	"strings"

	"trpc.group/trpc-go/trpc-go/codec"
)

const spaceIDMetadata = "X-Space-Id"

// callerSpace 返回调用方（经 Console/网关携带）声明的空间。没有空间元数据的调用（内部服务、因子引擎）不受空间限制；
// 多个不同的空间值视为非法，按拒绝处理。
func callerSpace(ctx context.Context) (space string, scoped bool, err error) {
	for key, value := range codec.Message(ctx).ServerMetaData() {
		if !strings.EqualFold(key, spaceIDMetadata) {
			continue
		}
		candidate := strings.TrimSpace(string(value))
		if scoped && candidate != space {
			return "", true, fmt.Errorf("conflicting space ids in request metadata")
		}
		space, scoped = candidate, true
	}
	if scoped && space == "" {
		return "", true, fmt.Errorf("space id in request metadata is empty")
	}
	return space, scoped, nil
}

// checkSetSpace 要求计算任务属于调用方声明的空间：Console 只校验了请求头里空间的权限，FactorMgr 的任务按全局 set_id 定位，
// 不校验归属会让一个空间的管理员读取、停用或删除其他空间的任务。
func (s *Service) checkSetSpace(ctx context.Context, setID string) error {
	space, scoped, err := callerSpace(ctx)
	if err != nil {
		return err
	}
	if !scoped {
		return nil
	}
	set, _, err := s.catalog.GetSet(ctx, setID)
	if err != nil {
		return err
	}
	if set.SpaceID != space {
		return fmt.Errorf("factor set %q does not belong to space %q", setID, space)
	}
	return nil
}

// checkJobSpace 通过 补算任务 → 计算任务 → 空间 校验归属。
func (s *Service) checkJobSpace(ctx context.Context, jobID string) error {
	if _, scoped, err := callerSpace(ctx); err != nil || !scoped {
		return err
	}
	job, err := s.recalc.Get(ctx, jobID)
	if err != nil {
		return err
	}
	return s.checkSetSpace(ctx, job.SetID)
}
