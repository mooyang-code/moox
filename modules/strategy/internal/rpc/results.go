package rpc

import (
	"context"
	"errors"
	"strings"

	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
)

// scopedInstance 读取实例并校验空间；已删除的实例仍可查看历史。
func (s *Service) scopedInstance(ctx context.Context, instanceID string) (store.Instance, error) {
	scoped, err := requireSpaceID(ctx)
	if err != nil {
		return store.Instance{}, err
	}
	instance, err := s.Store.GetInstance(ctx, instanceID)
	if err != nil {
		return store.Instance{}, err
	}
	if instance.SpaceID != scoped {
		return store.Instance{}, errors.New("实例不在当前空间")
	}
	return instance, nil
}

func (s *Service) ListStrategyResults(ctx context.Context, req *strategypb.ListStrategyResultsReq) (*strategypb.ListStrategyResultsRsp, error) {
	if err := s.ready(); err != nil {
		return &strategypb.ListStrategyResultsRsp{RetInfo: failure(err)}, nil
	}
	if req == nil || strings.TrimSpace(req.GetInstanceId()) == "" {
		return &strategypb.ListStrategyResultsRsp{RetInfo: invalid(errors.New("instance_id 不能为空"))}, nil
	}
	instance, err := s.scopedInstance(ctx, req.GetInstanceId())
	if err != nil {
		return &strategypb.ListStrategyResultsRsp{RetInfo: invalid(err)}, nil
	}
	page, size := pageValues(req.GetPage())
	results, total, err := s.Store.ListResults(ctx, instance.InstanceID, req.GetSessionId(), (page-1)*size, size)
	if err != nil {
		return &strategypb.ListStrategyResultsRsp{RetInfo: failure(err)}, nil
	}
	items := make([]*strategypb.StrategyResult, 0, len(results))
	for _, result := range results {
		items = append(items, resultProto(result))
	}
	return &strategypb.ListStrategyResultsRsp{RetInfo: success(), Results: items, Total: total, Page: int32(page), PageSize: int32(size)}, nil
}

func (s *Service) GetStrategyResult(ctx context.Context, req *strategypb.GetStrategyResultReq) (*strategypb.GetStrategyResultRsp, error) {
	if err := s.ready(); err != nil {
		return &strategypb.GetStrategyResultRsp{RetInfo: failure(err)}, nil
	}
	if req == nil || strings.TrimSpace(req.GetResultId()) == "" {
		return &strategypb.GetStrategyResultRsp{RetInfo: invalid(errors.New("result_id 不能为空"))}, nil
	}
	result, err := s.Store.GetResult(ctx, req.GetResultId())
	if err != nil {
		return &strategypb.GetStrategyResultRsp{RetInfo: failure(err)}, nil
	}
	if _, err := s.scopedInstance(ctx, result.InstanceID); err != nil {
		return &strategypb.GetStrategyResultRsp{RetInfo: invalid(err)}, nil
	}
	items, err := s.Store.ListResultItems(ctx, result.ResultID)
	if err != nil {
		return &strategypb.GetStrategyResultRsp{RetInfo: failure(err)}, nil
	}
	rsp := &strategypb.GetStrategyResultRsp{RetInfo: success(), Result: resultProto(result), Items: itemProtos(items)}
	// 快照确实不存在（旧数据）时留空，页面提示快照缺失；读取出错则整体失败：不能以成功返回不完整的解释，
	// 调用方会把成功的响应缓存起来，数据库恢复后也不再重试。
	session, err := s.Store.GetSession(ctx, result.SessionID)
	switch {
	case err == nil:
		rsp.ResolvedJson = session.ResolvedJSON
	case !errors.Is(err, store.ErrNotFound):
		return &strategypb.GetStrategyResultRsp{RetInfo: failure(err)}, nil
	}
	version, err := s.Store.GetDefinitionVersion(ctx, result.DSLHash)
	switch {
	case err == nil:
		rsp.DslYaml = version.DSLYaml
	case !errors.Is(err, store.ErrNotFound):
		return &strategypb.GetStrategyResultRsp{RetInfo: failure(err)}, nil
	}
	return rsp, nil
}

func (s *Service) ListStrategyTargets(ctx context.Context, req *strategypb.ListStrategyTargetsReq) (*strategypb.ListStrategyTargetsRsp, error) {
	if err := s.ready(); err != nil {
		return &strategypb.ListStrategyTargetsRsp{RetInfo: failure(err)}, nil
	}
	if req == nil || strings.TrimSpace(req.GetInstanceId()) == "" {
		return &strategypb.ListStrategyTargetsRsp{RetInfo: invalid(errors.New("instance_id 不能为空"))}, nil
	}
	instance, err := s.scopedInstance(ctx, req.GetInstanceId())
	if err != nil {
		return &strategypb.ListStrategyTargetsRsp{RetInfo: invalid(err)}, nil
	}
	if instance.SessionID == nil {
		return &strategypb.ListStrategyTargetsRsp{RetInfo: success(), Targets: []*strategypb.InstrumentTarget{}}, nil
	}
	latest, ok, err := s.Store.LatestOk(ctx, instance.InstanceID, *instance.SessionID)
	if err != nil {
		return &strategypb.ListStrategyTargetsRsp{RetInfo: failure(err)}, nil
	}
	if !ok {
		return &strategypb.ListStrategyTargetsRsp{RetInfo: success(), Targets: []*strategypb.InstrumentTarget{}, SessionId: *instance.SessionID}, nil
	}
	return &strategypb.ListStrategyTargetsRsp{RetInfo: success(), Targets: targetProtos(latest.TargetsJSON), SessionId: latest.SessionID, BarEndTime: formatTime(latest.BarEndTime), ValidUntil: formatTime(latest.ValidUntil), ResultId: latest.ResultID}, nil
}
