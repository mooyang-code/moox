package rpc

import (
	"context"
	"errors"
	"strings"

	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
)

// ConfirmStrategyInstance 把挂了组合账户、正在试运行的实例确认为实盘（批 4 T4、H7）：写入实例的确认列，下一期起目标不再带影子标记，
// Trade 按目标下单。没启用、没挂账户、策略池预热没完成时拒绝；已确认的再确认一次返回成功。与启停共用实例锁。
func (s *Service) ConfirmStrategyInstance(ctx context.Context, req *strategypb.ConfirmStrategyInstanceReq) (*strategypb.ConfirmStrategyInstanceRsp, error) {
	if err := s.ready(); err != nil {
		return &strategypb.ConfirmStrategyInstanceRsp{RetInfo: failure(err)}, nil
	}
	scoped, err := requireSpaceID(ctx)
	if err != nil {
		return &strategypb.ConfirmStrategyInstanceRsp{RetInfo: invalid(err)}, nil
	}
	if req == nil || strings.TrimSpace(req.GetInstanceId()) == "" {
		return &strategypb.ConfirmStrategyInstanceRsp{RetInfo: invalid(errors.New("instance_id 不能为空"))}, nil
	}
	instance, unlock, err := s.lockInstance(ctx, req.GetInstanceId())
	if err != nil {
		return &strategypb.ConfirmStrategyInstanceRsp{RetInfo: failure(err)}, nil
	}
	defer unlock()
	if instance.SpaceID != scoped {
		return &strategypb.ConfirmStrategyInstanceRsp{RetInfo: invalid(errors.New("实例不在当前空间"))}, nil
	}
	if err := s.confirmable(ctx, instance, strings.TrimSpace(req.GetSessionId())); err != nil {
		return &strategypb.ConfirmStrategyInstanceRsp{RetInfo: invalid(err), Instance: instanceProto(instance)}, nil
	}
	if instance.ConfirmedAt == nil {
		if err := s.Store.ConfirmInstance(ctx, instance.InstanceID, *instance.SessionID, s.nowTime()); err != nil {
			return &strategypb.ConfirmStrategyInstanceRsp{RetInfo: invalid(err), Instance: instanceProto(instance)}, nil
		}
	}
	confirmed, err := s.Store.GetInstance(ctx, instance.InstanceID)
	if err != nil {
		return &strategypb.ConfirmStrategyInstanceRsp{RetInfo: failure(err)}, nil
	}
	return &strategypb.ConfirmStrategyInstanceRsp{RetInfo: success(), Instance: instanceProto(confirmed)}, nil
}

// confirmable 检查实例能否确认实盘：启用中、挂了组合账户、会话与页面看到的一致，策略池的预热已经完成。
// 预热看会话里落库的预热终点（进程重启后也准），预热管理器只用来给出失败的原因。
func (s *Service) confirmable(ctx context.Context, instance store.Instance, sessionID string) error {
	if !instance.Enabled || instance.SessionID == nil {
		return errors.New("实例没有启用，不能确认实盘")
	}
	if instance.LogicalAccountID == nil {
		return errors.New("实例没有挂组合账户，只计算不下单，不需要确认实盘")
	}
	if sessionID != "" && sessionID != *instance.SessionID {
		return errors.New("实例已重新启用，请刷新后查看新会话的影子目标再确认")
	}
	session, err := s.Store.GetSession(ctx, *instance.SessionID)
	if err != nil {
		return err
	}
	if session.MembersJSON != "" && session.WarmedThrough == nil {
		if s.Warmer != nil {
			if reason := s.Warmer.Blocked(instance.InstanceID); reason != "" {
				return errors.New(reason + "，不能确认实盘")
			}
		}
		return errors.New("策略池预热未完成，不能确认实盘")
	}
	return nil
}
