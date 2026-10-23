package rpc

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
)

// instanceInScope 读取实例并确认它属于调用方的空间。
func (s *Service) instanceInScope(ctx context.Context, instanceID string) (store.Instance, error) {
	scoped, err := requireSpaceID(ctx)
	if err != nil {
		return store.Instance{}, err
	}
	if strings.TrimSpace(instanceID) == "" {
		return store.Instance{}, errors.New("instance_id 不能为空")
	}
	instance, err := s.Store.GetInstance(ctx, instanceID)
	if err != nil {
		return store.Instance{}, err
	}
	if instance.SpaceID != scoped || instance.DeletedAt != nil {
		return store.Instance{}, errors.New("实例不在当前空间")
	}
	return instance, nil
}

func displayProto(hidden store.DisplayHidden) *strategypb.DisplayHidden {
	return &strategypb.DisplayHidden{Categories: hidden.Categories, Members: hidden.Members, Columns: hidden.Columns}
}

// GetStrategyDisplaySettings 读取实例的外显设置（隐藏的分类、成员与列；默认全部展示）。
func (s *Service) GetStrategyDisplaySettings(ctx context.Context, req *strategypb.GetStrategyDisplaySettingsReq) (*strategypb.GetStrategyDisplaySettingsRsp, error) {
	if err := s.ready(); err != nil {
		return &strategypb.GetStrategyDisplaySettingsRsp{RetInfo: failure(err)}, nil
	}
	instance, err := s.instanceInScope(ctx, req.GetInstanceId())
	if err != nil {
		return &strategypb.GetStrategyDisplaySettingsRsp{RetInfo: invalid(err)}, nil
	}
	hidden, err := s.Store.GetDisplayHidden(ctx, instance.InstanceID)
	if err != nil {
		return &strategypb.GetStrategyDisplaySettingsRsp{RetInfo: failure(err)}, nil
	}
	return &strategypb.GetStrategyDisplaySettingsRsp{RetInfo: success(), Hidden: displayProto(hidden)}, nil
}

// SetStrategyDisplaySettings 保存实例的外显设置。外显设置不进会话快照与哈希，改了立即生效。
func (s *Service) SetStrategyDisplaySettings(ctx context.Context, req *strategypb.SetStrategyDisplaySettingsReq) (*strategypb.SetStrategyDisplaySettingsRsp, error) {
	if err := s.ready(); err != nil {
		return &strategypb.SetStrategyDisplaySettingsRsp{RetInfo: failure(err)}, nil
	}
	instance, err := s.instanceInScope(ctx, req.GetInstanceId())
	if err != nil {
		return &strategypb.SetStrategyDisplaySettingsRsp{RetInfo: invalid(err)}, nil
	}
	hidden, err := s.Store.SetDisplayHidden(ctx, instance.InstanceID, store.DisplayHidden{
		Categories: req.GetHidden().GetCategories(), Members: req.GetHidden().GetMembers(), Columns: req.GetHidden().GetColumns(),
	}, s.nowTime())
	if err != nil {
		return &strategypb.SetStrategyDisplaySettingsRsp{RetInfo: invalid(err)}, nil
	}
	return &strategypb.SetStrategyDisplaySettingsRsp{RetInfo: success(), Hidden: displayProto(hidden)}, nil
}

// ListStrategyParamSubmissions 返回实例最近的因子参数提交记录（目标期次倒序）。
func (s *Service) ListStrategyParamSubmissions(ctx context.Context, req *strategypb.ListStrategyParamSubmissionsReq) (*strategypb.ListStrategyParamSubmissionsRsp, error) {
	if err := s.ready(); err != nil {
		return &strategypb.ListStrategyParamSubmissionsRsp{RetInfo: failure(err)}, nil
	}
	instance, err := s.instanceInScope(ctx, req.GetInstanceId())
	if err != nil {
		return &strategypb.ListStrategyParamSubmissionsRsp{RetInfo: invalid(err)}, nil
	}
	submissions, err := s.Store.ListParamSubmissions(ctx, instance.InstanceID, int(req.GetLimit()))
	if err != nil {
		return &strategypb.ListStrategyParamSubmissionsRsp{RetInfo: failure(err)}, nil
	}
	rsp := &strategypb.ListStrategyParamSubmissionsRsp{RetInfo: success()}
	for _, item := range submissions {
		rsp.Submissions = append(rsp.Submissions, &strategypb.ParamSubmission{
			RequestId: item.RequestID, InstanceId: item.InstanceID, ResultId: item.ResultID, SetId: item.SetID,
			PeriodTime: item.PeriodTime.UnixMilli(), BasedOnPeriod: item.BasedOnPeriod.UnixMilli(), ParamsJson: string(item.ParamsJSON),
			Notes: item.Notes, Status: item.Status, Attempts: int32(item.Attempts), OutcomeJson: string(item.OutcomeJSON),
			CreatedAt: item.CreatedAt.Format(time.RFC3339), UpdatedAt: item.UpdatedAt.Format(time.RFC3339),
		})
	}
	return rsp, nil
}
