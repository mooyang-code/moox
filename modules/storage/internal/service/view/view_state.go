package view

import (
	"context"
	"errors"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	"github.com/mooyang-code/moox/modules/storage/internal/service/view/follow"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

const (
	defaultWaitViewApplied = 10 * time.Second
	maxWaitViewApplied     = 30 * time.Second
)

// GetViewState 返回 View 当前的索引状态与跟随变更日志已扫描到的位置。
func (s *Service) GetViewState(ctx context.Context, req *pb.GetViewStateReq) (*pb.GetViewStateRsp, error) {
	if req == nil || req.GetSpaceId() == "" || req.GetDatasetId() == "" {
		return &pb.GetViewStateRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id 与 dataset_id 不能为空"))}, nil
	}
	ref := datasetRef{spaceID: req.GetSpaceId(), datasetID: req.GetDatasetId()}
	runtime := s.runtimeOf(ref)
	if runtime == nil {
		return &pb.GetViewStateRsp{RetInfo: retinfo.Error(pb.ErrorCode_VIEW_NOT_FOUND, errors.New("数据集不存在、已停用或还没有开始建索引"))}, nil
	}
	state := runtime.queryState()
	if !state.active() {
		return &pb.GetViewStateRsp{RetInfo: retinfo.Success("success")}, nil
	}
	rsp := &pb.GetViewStateRsp{
		RetInfo: retinfo.Success("success"), IndexId: runtime.indexID,
		ViewRevision: state.schema.ViewVersion, ViewSchemaHash: state.schema.SchemaHash, Columns: resultColumns(state.schema.Columns, state.spec),
	}
	stats, err := restoreIndexStats(ctx, runtime.engine, runtime.indexID)
	if err != nil {
		return &pb.GetViewStateRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	rsp.IndexedFrom, rsp.IndexedTo = stats.IndexedFrom, stats.IndexedTo
	if cached, ok := cachedActiveIndexStats(runtime, runtime.indexID); ok && cached.IndexedFrom != "" {
		rsp.IndexedFrom, rsp.IndexedTo = cached.IndexedFrom, cached.IndexedTo
	}
	followState := stats.Follow
	if status, ok := s.followManager().Status(ref.spaceID, ref.datasetID); ok && status.Loaded {
		// 内存里的进度比落盘的新（只扫描、没有写入的前进每分钟才落盘一次）。
		followState = status.State
	}
	rsp.Generation = followState.BuiltSequence
	rsp.AppliedCursor = changeCursorPB(followState.Cursor)
	return rsp, nil
}

// WaitViewApplied 等 View 跟上调用这一刻的变更日志头部。
func (s *Service) WaitViewApplied(ctx context.Context, req *pb.WaitViewAppliedReq) (*pb.WaitViewAppliedRsp, error) {
	if req == nil || req.GetSpaceId() == "" || req.GetDatasetId() == "" {
		return &pb.WaitViewAppliedRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id 与 dataset_id 不能为空"))}, nil
	}
	timeout := defaultWaitViewApplied
	if req.GetWaitTimeoutMs() > 0 {
		timeout = min(time.Duration(req.GetWaitTimeoutMs())*time.Millisecond, maxWaitViewApplied)
	}
	manager := s.followManager()
	if manager == nil {
		return &pb.WaitViewAppliedRsp{RetInfo: retinfo.Error(pb.ErrorCode_VIEW_NOT_READY, follow.ErrNotFollowing)}, nil
	}
	applied, target, current, err := manager.WaitApplied(ctx, req.GetSpaceId(), req.GetDatasetId(), timeout)
	if errors.Is(err, follow.ErrNotFollowing) {
		return &pb.WaitViewAppliedRsp{RetInfo: retinfo.Error(pb.ErrorCode_VIEW_NOT_READY, err)}, nil
	}
	if err != nil {
		return &pb.WaitViewAppliedRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	return &pb.WaitViewAppliedRsp{RetInfo: retinfo.Success("success"), Applied: applied, Target: changeCursorPB(target), AppliedCursor: changeCursorPB(current)}, nil
}

func changeCursorPB(cursor viewindex.ChangeCursor) *pb.DatasetChangeCursor {
	if cursor.StoreID == "" {
		return nil
	}
	return &pb.DatasetChangeCursor{StoreId: cursor.StoreID, Sequence: cursor.Sequence}
}

// resultColumns 把活动索引已建的列转成对外的列定义，显示名取自数据集列。
func resultColumns(columns []*viewindex.Column, spec *viewSpec) []*pb.ResultColumn {
	out := make([]*pb.ResultColumn, 0, len(columns))
	for _, column := range columns {
		if column == nil {
			continue
		}
		result := &pb.ResultColumn{ColumnName: column.ColumnName, ValueType: column.ValueType}
		if spec != nil {
			result.DisplayName = spec.DisplayNames[column.ColumnName]
		}
		out = append(out, result)
	}
	return out
}
