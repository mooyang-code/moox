package rpc

import (
	"context"
	"errors"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"trpc.group/trpc-go/trpc-go/log"
)

// ReregisterFetchPlan 在 Storage 数据被清空后让采集计划重新生效，见 proto 里的说明。
// 先清计划：它检查“没有未生效的计划”这个前提；通过之后再清补采状态。水位留空即可：hash 清空后数据集的时间线
// 从重新登记的新生效周期开始（见 klinecheck.BuildTimeline），自动补采从那里扫起，不会回补清库前的历史。
func (s *Service) ReregisterFetchPlan(ctx context.Context, req *pb.ReregisterFetchPlanReq) (*pb.ReregisterFetchPlanRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" {
		return &pb.ReregisterFetchPlanRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	spaceID := strings.TrimSpace(req.GetSpaceId())
	datasets, err := marketfetch.ClearRegisteredSnapshots(ctx, s.persistence.FetchPlans(), spaceID)
	switch {
	case errors.Is(err, marketfetch.ErrReregisterPlanOpen):
		return &pb.ReregisterFetchPlanRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "该空间还有未生效的采集计划，等它生效后再重新登记（moox-cli collector fetch-plan 查看进度）")}, nil
	case errors.Is(err, marketfetch.ErrReregisterNoPlan):
		return &pb.ReregisterFetchPlanRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "该空间还没有已生效的采集计划，没有需要重新登记的数据集")}, nil
	case err != nil:
		log.ErrorContextf(ctx, "reregister fetch plan failed space=%s: %v", spaceID, err)
		return &pb.ReregisterFetchPlanRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	jobs, err := s.persistence.Backfill().Reset(ctx, spaceID)
	if err != nil {
		log.ErrorContextf(ctx, "reset backfill state failed space=%s: %v", spaceID, err)
		return &pb.ReregisterFetchPlanRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "采集计划已标记重新登记，但清理补采状态失败："+err.Error())}, nil
	}
	log.InfoContextf(ctx, "collector fetch plan marked for re-registration space=%s datasets=%d backfill_jobs=%d", spaceID, len(datasets), jobs)
	return &pb.ReregisterFetchPlanRsp{RetInfo: retOK(), Datasets: datasets, BackfillJobs: int32(jobs)}, nil
}

// FetchPlanControl 是采集计划管理器对 RPC 暴露的状态查询。
type FetchPlanControl interface {
	TimersDisabled(space string) bool
}

// SetFetchPlanControl 注入采集计划管理器；没有配置采集计划的部署不注入。
func (s *Service) SetFetchPlanControl(control FetchPlanControl) { s.fetchPlanControl = control }

// SetFetchPlanPaused 暂停或恢复空间的采集，见 proto 里的说明。管理器每分钟同步一次空间集合，
// 所以保存开关后要过约 1 分钟才会开始停用定时器；调用方轮询 timers_disabled 等待确认。
func (s *Service) SetFetchPlanPaused(ctx context.Context, req *pb.SetFetchPlanPausedReq) (*pb.SetFetchPlanPausedRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" {
		return &pb.SetFetchPlanPausedRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	spaceID := strings.TrimSpace(req.GetSpaceId())
	if s.fetchPlanControl == nil {
		return &pb.SetFetchPlanPausedRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "Collector 没有启用采集计划，没有可暂停的空间")}, nil
	}
	if err := s.persistence.FetchPlans().SetPaused(ctx, spaceID, req.GetPaused()); err != nil {
		log.ErrorContextf(ctx, "set fetch plan paused failed space=%s paused=%t: %v", spaceID, req.GetPaused(), err)
		return &pb.SetFetchPlanPausedRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	if req.GetPaused() {
		log.InfoContextf(ctx, "collector fetch plan paused space=%s", spaceID)
	} else {
		log.InfoContextf(ctx, "collector fetch plan resumed space=%s", spaceID)
	}
	return &pb.SetFetchPlanPausedRsp{RetInfo: retOK(), Paused: req.GetPaused(), TimersDisabled: req.GetPaused() && s.fetchPlanControl.TimersDisabled(spaceID)}, nil
}
