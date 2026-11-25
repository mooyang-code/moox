package rpc

import (
	"context"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/klinecheck"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"trpc.group/trpc-go/trpc-go/log"
)

func historySeedsToPB(views []klinecheck.SeedView) []*pb.HistorySeed {
	out := make([]*pb.HistorySeed, 0, len(views))
	for _, view := range views {
		item := &pb.HistorySeed{
			SpaceId: view.SpaceID, DatasetId: view.DatasetID, Frequency: view.Frequency, Status: view.Status,
			TargetFrom: view.TargetFrom.Format(time.RFC3339), EffectiveFrom: view.EffectiveFrom.Format(time.RFC3339),
			TotalChunks: int32(view.TotalChunks), DoneChunks: int32(view.DoneChunks), FailedChunks: int32(view.FailedChunks),
			EtaSeconds: int64(view.ETA / time.Second), StartedAt: view.StartedAt.Format(time.RFC3339), Note: view.Note,
		}
		if view.FinishedAt != nil {
			item.FinishedAt = view.FinishedAt.UTC().Format(time.RFC3339)
		}
		out = append(out, item)
	}
	return out
}

// ListHistorySeeds 返回历史补齐记录；space_id 为空返回全部空间，only_running 只返回补齐中的。
func (s *Service) ListHistorySeeds(ctx context.Context, req *pb.ListHistorySeedsReq) (*pb.ListHistorySeedsRsp, error) {
	if s.klineOps == nil {
		return &pb.ListHistorySeedsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "kline backfill is not configured")}, nil
	}
	views, err := s.klineOps.SeedViews(ctx, strings.TrimSpace(req.GetSpaceId()), req.GetOnlyRunning())
	if err != nil {
		log.ErrorContextf(ctx, "list history seeds failed space=%s: %v", req.GetSpaceId(), err)
		return &pb.ListHistorySeedsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	return &pb.ListHistorySeedsRsp{RetInfo: retOK(), Seeds: historySeedsToPB(views)}, nil
}

// SkipHistorySeed 手动放行补齐中的数据集：取消剩余分块，已补进去的数据保留。
func (s *Service) SkipHistorySeed(ctx context.Context, req *pb.SkipHistorySeedReq) (*pb.SkipHistorySeedRsp, error) {
	if s.klineOps == nil {
		return &pb.SkipHistorySeedRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "kline backfill is not configured")}, nil
	}
	spaceID := strings.TrimSpace(req.GetSpaceId())
	if spaceID == "" {
		return &pb.SkipHistorySeedRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	skipped, err := s.klineOps.SkipSeeds(ctx, spaceID, strings.TrimSpace(req.GetDatasetId()))
	if err != nil {
		log.ErrorContextf(ctx, "skip history seed failed space=%s dataset=%s: %v", spaceID, req.GetDatasetId(), err)
		return &pb.SkipHistorySeedRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	return &pb.SkipHistorySeedRsp{RetInfo: retOK(), Skipped: int32(skipped)}, nil
}
