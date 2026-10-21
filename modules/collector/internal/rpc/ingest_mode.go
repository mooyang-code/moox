package rpc

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
)

// SetStreamService 注入推送采集服务（运行状态、切换前的在线检查）。
func (s *Service) SetStreamService(stream *StreamService) { s.stream = stream }

// GetIngestMode 返回数据集的采集方式、正在交接的生效周期与 collector-stream 实例状态。
func (s *Service) GetIngestMode(ctx context.Context, req *pb.GetIngestModeReq) (*pb.GetIngestModeRsp, error) {
	spaceID := normalizeSpace(req.GetSpaceId())
	if spaceID == "" {
		return &pb.GetIngestModeRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id 必填")}, nil
	}
	modes, err := s.ingestModes(ctx, spaceID, strings.TrimSpace(req.GetDatasetId()))
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

// ingestModes 列出数据集的采集方式，并标出正在交接的数据集各频率的生效周期 P。
func (s *Service) ingestModes(ctx context.Context, spaceID, datasetID string) ([]*pb.IngestMode, error) {
	stored, err := s.persistence.IngestModes().List(ctx, spaceID)
	if err != nil {
		return nil, err
	}
	periods := map[string]map[string]string{}
	open, err := s.persistence.FetchPlans().Open(ctx, spaceID)
	if err != nil {
		return nil, err
	}
	if open != nil {
		content, err := marketfetch.DecodeFetchPlanContent(open.Plan)
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
	ids := make([]string, 0, len(stored))
	for id := range stored {
		if datasetID == "" || id == datasetID {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	result := make([]*pb.IngestMode, 0, len(ids))
	for _, id := range ids {
		mode := stored[id]
		result = append(result, &pb.IngestMode{SpaceId: spaceID, DatasetId: id, Mode: mode.Mode, Executor: mode.Executor, UpdatedAt: mode.UpdatedAt.UTC().Format(time.RFC3339), EffectivePeriods: periods[id]})
	}
	return result, nil
}

// SetIngestMode 按数据集切换采集方式（只允许 moox-cli）。下一轮对账生成交接计划并按频率选生效周期 P。
func (s *Service) SetIngestMode(ctx context.Context, req *pb.SetIngestModeReq) (*pb.SetIngestModeRsp, error) {
	spaceID := normalizeSpace(req.GetSpaceId())
	datasetID := strings.TrimSpace(req.GetDatasetId())
	mode, executor := strings.ToLower(strings.TrimSpace(req.GetMode())), strings.ToLower(strings.TrimSpace(req.GetExecutor()))
	if spaceID == "" {
		return &pb.SetIngestModeRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id 必填")}, nil
	}
	if mode != store.IngestStream && mode != store.IngestPull {
		return &pb.SetIngestModeRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "mode 必须是 stream 或 pull")}, nil
	}
	if executor != "" && executor != store.ExecutorSCF && executor != store.ExecutorLocal {
		return &pb.SetIngestModeRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "executor 必须是 scf 或 local")}, nil
	}
	stored, err := s.persistence.IngestModes().List(ctx, spaceID)
	if err != nil {
		return &pb.SetIngestModeRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	var targets []string
	for id := range stored {
		if datasetID == "" || id == datasetID {
			targets = append(targets, id)
		}
	}
	if len(targets) == 0 {
		return &pb.SetIngestModeRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, fmt.Sprintf("空间 %s 没有已登记采集方式的数据集 %s（数据集在第一次对账时登记）", spaceID, datasetID))}, nil
	}
	sort.Strings(targets)
	var worker *pb.StreamWorkerStatus
	if s.stream != nil {
		if worker, err = s.stream.WorkerStatus(ctx, spaceID); err != nil {
			return &pb.SetIngestModeRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
		}
	}
	// 切到 stream（或用本地执行端）要求 collector-stream 在线，否则拒绝。
	if mode == store.IngestStream || executor == store.ExecutorLocal {
		if worker == nil || !worker.GetOnline() {
			return &pb.SetIngestModeRsp{RetInfo: retErr(pb.ErrorCode_CONFLICT, "collector-stream 不在线（心跳超时），不能切到 stream 或本地执行端"), Stream: worker}, nil
		}
	}
	updates := make([]store.IngestMode, 0, len(targets))
	for _, id := range targets {
		next := store.IngestMode{SpaceID: spaceID, DatasetID: id, Mode: mode, Executor: executor, Force: req.GetForce()}
		// 不指定执行端时：切到 stream 一并把补采执行端设为 local（本地执行端与推送同进程），切到 pull 用 scf。
		if next.Executor == "" {
			next.Executor = store.ExecutorLocal
			if mode == store.IngestPull {
				next.Executor = store.ExecutorSCF
			}
		}
		updates = append(updates, next)
	}
	if err := s.persistence.IngestModes().Set(ctx, updates); err != nil {
		return &pb.SetIngestModeRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	if req.GetForce() {
		// 已打开的计划如果正等着失联的推送或本地执行端确认，也不再等：按它已登记的 hash 与生效周期推进完成，再开始交接。
		if err := marketfetch.MarkOpenPlanForced(ctx, s.persistence.FetchPlans(), spaceID); err != nil {
			return &pb.SetIngestModeRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
		}
	}
	modes, err := s.ingestModes(ctx, spaceID, datasetID)
	if err != nil {
		return &pb.SetIngestModeRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	rsp := &pb.SetIngestModeRsp{RetInfo: retOK(), Modes: modes, Stream: worker}
	if open, err := s.persistence.FetchPlans().Open(ctx, spaceID); err == nil && open != nil {
		rsp.PlanId = open.ID
	}
	return rsp, nil
}
