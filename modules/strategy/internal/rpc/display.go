package rpc

import (
	"context"
	"errors"
	"strings"

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
	if instance.SpaceID != scoped {
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
