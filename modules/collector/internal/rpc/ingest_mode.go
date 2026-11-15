package rpc

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/fetchplan"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
)

// SetStreamService 注入推送采集服务（运行状态、切换前的在线检查）。
func (s *Service) SetStreamService(stream *StreamService) { s.stream = stream }

// GetIngestMode 返回 K 线任务的采集方式、正在交接的生效周期与 collector-stream 实例状态。
func (s *Service) GetIngestMode(ctx context.Context, req *pb.GetIngestModeReq) (*pb.GetIngestModeRsp, error) {
	spaceID := normalizeSpace(req.GetSpaceId())
	if spaceID == "" {
		return &pb.GetIngestModeRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id 必填")}, nil
	}
	modes, err := s.ingestModes(ctx, spaceID, strings.TrimSpace(req.GetDatasetId()), false)
	if err != nil {
		return &pb.GetIngestModeRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	rsp := &pb.GetIngestModeRsp{RetInfo: retOK(), Modes: modes}
	if s.stream != nil {
		if rsp.Stream, err = s.stream.WorkerStatus(ctx, spaceID); err != nil {
			return &pb.GetIngestModeRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
		}
	}
	return rsp, nil
}

// ingestModes 列出 K 线任务的采集方式，并标出正在交接的数据集各频率的生效周期 P。enabledOnly 为真时只列启用的任务。
func (s *Service) ingestModes(ctx context.Context, spaceID, datasetID string, enabledOnly bool) ([]*pb.IngestMode, error) {
	tasks, err := s.taskRepo.ListKline(ctx, spaceID, datasetID)
	if err != nil {
		return nil, err
	}
	periods := map[string]map[string]string{}
	open, err := s.persistence.FetchPlans().Open(ctx, spaceID)
	if err != nil {
		return nil, err
	}
	if open != nil {
		content, err := fetchplan.DecodeFetchPlanContent(open.Plan)
		if err != nil {
			return nil, err
		}
		for _, change := range content.Datasets {
			if change.Changed && change.OldIngest != "" && change.P > 0 {
				if periods[change.DatasetID] == nil {
					periods[change.DatasetID] = map[string]string{}
				}
				periods[change.DatasetID][change.Frequency] = time.Unix(change.P, 0).UTC().Format(time.RFC3339)
			}
		}
	}
	result := make([]*pb.IngestMode, 0, len(tasks))
	for _, task := range tasks {
		if enabledOnly && !task.Enabled {
			continue
		}
		result = append(result, &pb.IngestMode{SpaceId: spaceID, DatasetId: task.ResultDatasetID, TaskId: task.TaskID, Ingest: task.Ingest, EffectivePeriods: periods[task.ResultDatasetID]})
	}
	return result, nil
}

// SetIngestMode 切换 K 线任务的采集方式（只允许 moox-cli）：按数据集切换时改写这个数据集的任务，不给数据集时改空间的全部 K 线任务。
// 下一轮对账生成交接计划并按频率选生效周期 P。
func (s *Service) SetIngestMode(ctx context.Context, req *pb.SetIngestModeReq) (*pb.SetIngestModeRsp, error) {
	spaceID := normalizeSpace(req.GetSpaceId())
	datasetID := strings.TrimSpace(req.GetDatasetId())
	ingest := strings.ToLower(strings.TrimSpace(req.GetIngest()))
	if spaceID == "" {
		return &pb.SetIngestModeRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id 必填")}, nil
	}
	if err := domain.ValidateIngest(ingest); err != nil {
		return &pb.SetIngestModeRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	var worker *pb.StreamWorkerStatus
	var err error
	if s.stream != nil {
		if worker, err = s.stream.WorkerStatus(ctx, spaceID); err != nil {
			return &pb.SetIngestModeRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
		}
	}
	// 切到 stream 或 local 要求 collector-stream 在线，否则拒绝。
	if ingest != domain.IngestSCF && (worker == nil || !worker.GetOnline()) {
		return &pb.SetIngestModeRsp{RetInfo: retErr(pb.ErrorCode_CONFLICT, "collector-stream 不在线（心跳超时），不能切到 stream 或 local"), Stream: worker}, nil
	}
	changed, err := s.taskRepo.SetIngest(ctx, spaceID, datasetID, ingest)
	if err != nil {
		return &pb.SetIngestModeRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	if changed == 0 {
		return &pb.SetIngestModeRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, fmt.Sprintf("空间 %s 没有可切换的 K 线任务（数据集 %q）；用 moox-cli collector ingest-mode get 看现有任务", spaceID, datasetID))}, nil
	}
	modes, err := s.ingestModes(ctx, spaceID, datasetID, false)
	if err != nil {
		return &pb.SetIngestModeRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	rsp := &pb.SetIngestModeRsp{RetInfo: retOK(), Modes: modes, Stream: worker}
	if open, err := s.persistence.FetchPlans().Open(ctx, spaceID); err == nil && open != nil {
		rsp.PlanId = open.ID
	}
	return rsp, nil
}
