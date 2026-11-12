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
	if req == nil || req.GetSpaceId() == "" || req.GetViewId() == "" {
		return &pb.GetViewStateRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id 与 view_id 不能为空"))}, nil
	}
	if err := s.authorize(req.GetAuthInfo()); err != nil {
		return &pb.GetViewStateRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	ref := viewRef{spaceID: req.GetSpaceId(), viewID: req.GetViewId()}
	s.mu.RLock()
	runtime := s.views[ref]
	_, known := s.catalogViews[ref]
	s.mu.RUnlock()
	if runtime == nil || !known {
		return &pb.GetViewStateRsp{RetInfo: retinfo.Error(pb.ErrorCode_VIEW_NOT_FOUND, errors.New("View 不存在或还没有开始建索引"))}, nil
	}
	state := runtime.queryState()
	rsp := &pb.GetViewStateRsp{RetInfo: retinfo.Success("success"), IndexId: state.active}
	if state.active == "" {
		return rsp, nil
	}
	s.mu.RLock()
	schema := s.schemas[state.active]
	s.mu.RUnlock()
	rsp.ViewRevision, rsp.ViewSchemaHash, rsp.Columns = schema.ViewVersion, schema.SchemaHash, schema.Columns
	engine, err := s.engineFor(state.active)
	if err != nil {
		return &pb.GetViewStateRsp{RetInfo: retinfo.Error(queryErrorCode(err), err)}, nil
	}
	stats, err := restoreIndexStats(ctx, engine, state.active)
	if err != nil {
		return &pb.GetViewStateRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	rsp.IndexedFrom, rsp.IndexedTo = stats.IndexedFrom, stats.IndexedTo
	if cached, ok := cachedActiveIndexStats(runtime, state.active); ok && cached.IndexedFrom != "" {
		rsp.IndexedFrom, rsp.IndexedTo = cached.IndexedFrom, cached.IndexedTo
	}
	followState := stats.Follow
	if status, ok := s.followManager().Status(ref.spaceID, ref.viewID); ok && status.Loaded {
		// 内存里的进度比落盘的新（只扫描、没有写入的前进每分钟才落盘一次）。
		followState = status.State
	}
	rsp.Generation = followState.BuiltSequence
	rsp.AppliedCursor = changeCursorPB(followState.Cursor)
	return rsp, nil
}

// WaitViewApplied 等 View 跟上调用这一刻的变更日志头部。
func (s *Service) WaitViewApplied(ctx context.Context, req *pb.WaitViewAppliedReq) (*pb.WaitViewAppliedRsp, error) {
	if req == nil || req.GetSpaceId() == "" || req.GetViewId() == "" {
		return &pb.WaitViewAppliedRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id 与 view_id 不能为空"))}, nil
	}
	if err := s.authorize(req.GetAuthInfo()); err != nil {
		return &pb.WaitViewAppliedRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	timeout := defaultWaitViewApplied
	if req.GetWaitTimeoutMs() > 0 {
		timeout = min(time.Duration(req.GetWaitTimeoutMs())*time.Millisecond, maxWaitViewApplied)
	}
	manager := s.followManager()
	if manager == nil {
		return &pb.WaitViewAppliedRsp{RetInfo: retinfo.Error(pb.ErrorCode_VIEW_NOT_READY, follow.ErrNotFollowing)}, nil
	}
	applied, target, current, err := manager.WaitApplied(ctx, req.GetSpaceId(), req.GetViewId(), timeout)
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
