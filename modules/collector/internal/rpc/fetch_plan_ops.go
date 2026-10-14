package rpc

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"trpc.group/trpc-go/trpc-go/log"
)

// ReregisterFetchPlan 在 Storage 数据被清空后让采集计划重新生效，见 proto 里的说明。
// 先清计划：它检查“没有未生效的计划”这个前提；通过之后再清补采状态并把各数据集的自动补采水位推到现在，
// 否则水位为空时自动补采会从最早的计划起点开始回补历史。
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
	backfill := s.persistence.Backfill()
	jobs, err := backfill.Reset(ctx, spaceID)
	if err != nil {
		log.ErrorContextf(ctx, "reset backfill state failed space=%s: %v", spaceID, err)
		return &pb.ReregisterFetchPlanRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "采集计划已标记重新登记，但清理补采状态失败："+err.Error())}, nil
	}
	now := time.Now().UTC()
	for _, key := range datasets {
		datasetID, frequency, _ := strings.Cut(key, "|")
		if err := backfill.SetWatermark(ctx, spaceID, datasetID, frequency, now); err != nil {
			log.ErrorContextf(ctx, "set backfill watermark failed space=%s dataset=%s: %v", spaceID, key, err)
			return &pb.ReregisterFetchPlanRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "采集计划已标记重新登记，但重置补采水位失败："+err.Error())}, nil
		}
	}
	log.InfoContextf(ctx, "collector fetch plan marked for re-registration space=%s datasets=%d backfill_jobs=%d", spaceID, len(datasets), jobs)
	return &pb.ReregisterFetchPlanRsp{RetInfo: retOK(), Datasets: datasets, BackfillJobs: int32(jobs)}, nil
}
