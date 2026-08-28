package primarystore

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type markerDataNodeClient interface {
	AppendDatasetPeriodCollected(context.Context, *pb.AppendDatasetPeriodCollectedReq) (*pb.AppendDatasetPeriodCollectedRsp, error)
	AppendFactorPeriodComputed(context.Context, *pb.AppendFactorPeriodComputedReq) (*pb.AppendFactorPeriodComputedRsp, error)
	AppendDatasetSyncPointMarker(context.Context, *pb.AppendDatasetSyncPointMarkerReq) (*pb.AppendDatasetSyncPointMarkerRsp, error)
	GetFactorPeriodComputedMarker(context.Context, *pb.GetFactorPeriodComputedMarkerReq) (*pb.GetFactorPeriodComputedMarkerRsp, error)
}

func (s *Service) ReportDatasetPeriodCollected(ctx context.Context, req *pb.ReportDatasetPeriodCollectedReq) (*pb.ReportDatasetPeriodCollectedRsp, error) {
	if err := rejectMooxSkillWrite(req.GetAuthInfo()); err != nil {
		return &pb.ReportDatasetPeriodCollectedRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	if err := s.validateMarkerCaller(req.GetAuthInfo(), req.GetSpaceId(), req.GetMarker().GetDatasetId(), "collector"); err != nil {
		return &pb.ReportDatasetPeriodCollectedRsp{RetInfo: markerError(err)}, nil
	}
	ctx = s.requestContext(ctx)
	node, err := s.resolve(ctx, req.GetSpaceId(), req.GetMarker().GetDatasetId())
	if err != nil {
		return &pb.ReportDatasetPeriodCollectedRsp{RetInfo: markerError(err)}, nil
	}
	markerNode, ok := node.(markerDataNodeClient)
	if !ok {
		return &pb.ReportDatasetPeriodCollectedRsp{RetInfo: markerError(errors.New("DataNode marker RPC is unavailable"))}, nil
	}
	auth, err := s.signAuth(req.GetAuthInfo())
	if err != nil {
		return &pb.ReportDatasetPeriodCollectedRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	rsp, err := markerNode.AppendDatasetPeriodCollected(ctx, &pb.AppendDatasetPeriodCollectedReq{AuthInfo: auth, SpaceId: req.GetSpaceId(), Marker: req.GetMarker()})
	if err != nil {
		return &pb.ReportDatasetPeriodCollectedRsp{RetInfo: markerError(err)}, nil
	}
	return &pb.ReportDatasetPeriodCollectedRsp{RetInfo: rsp.GetRetInfo(), EventId: rsp.GetEventId()}, nil
}

func (s *Service) ReportFactorPeriodComputed(ctx context.Context, req *pb.ReportFactorPeriodComputedReq) (*pb.ReportFactorPeriodComputedRsp, error) {
	if err := rejectMooxSkillWrite(req.GetAuthInfo()); err != nil {
		return &pb.ReportFactorPeriodComputedRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	if err := s.validateMarkerCaller(req.GetAuthInfo(), req.GetSpaceId(), req.GetMarker().GetResultDatasetId(), "factor"); err != nil {
		return &pb.ReportFactorPeriodComputedRsp{RetInfo: markerError(err)}, nil
	}
	ctx = s.requestContext(ctx)
	node, err := s.resolve(ctx, req.GetSpaceId(), req.GetMarker().GetResultDatasetId())
	if err != nil {
		return &pb.ReportFactorPeriodComputedRsp{RetInfo: markerError(err)}, nil
	}
	markerNode, ok := node.(markerDataNodeClient)
	if !ok {
		return &pb.ReportFactorPeriodComputedRsp{RetInfo: markerError(errors.New("DataNode marker RPC is unavailable"))}, nil
	}
	auth, err := s.signAuth(req.GetAuthInfo())
	if err != nil {
		return &pb.ReportFactorPeriodComputedRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	rsp, err := markerNode.AppendFactorPeriodComputed(ctx, &pb.AppendFactorPeriodComputedReq{AuthInfo: auth, SpaceId: req.GetSpaceId(), Marker: req.GetMarker()})
	if err != nil {
		return &pb.ReportFactorPeriodComputedRsp{RetInfo: markerError(err)}, nil
	}
	return &pb.ReportFactorPeriodComputedRsp{RetInfo: rsp.GetRetInfo(), EventId: rsp.GetEventId()}, nil
}

func (s *Service) AppendDatasetSyncPoint(ctx context.Context, req *pb.AppendDatasetSyncPointReq) (*pb.AppendDatasetSyncPointRsp, error) {
	if err := rejectMooxSkillWrite(req.GetAuthInfo()); err != nil {
		return &pb.AppendDatasetSyncPointRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	marker := req.GetSyncPoint()
	if err := s.validateMarkerCaller(req.GetAuthInfo(), req.GetSpaceId(), marker.GetDatasetId(), ""); err != nil {
		return &pb.AppendDatasetSyncPointRsp{RetInfo: markerError(err)}, nil
	}
	if marker.GetRequestId() == "" || (marker.GetSource() != "import" && marker.GetSource() != "catchup") {
		return &pb.AppendDatasetSyncPointRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("request_id and source import|catchup are required"))}, nil
	}
	ctx = s.requestContext(ctx)
	node, err := s.resolve(ctx, req.GetSpaceId(), marker.GetDatasetId())
	if err != nil {
		return &pb.AppendDatasetSyncPointRsp{RetInfo: markerError(err)}, nil
	}
	markerNode, ok := node.(markerDataNodeClient)
	if !ok {
		return &pb.AppendDatasetSyncPointRsp{RetInfo: markerError(errors.New("DataNode marker RPC is unavailable"))}, nil
	}
	auth, err := s.signAuth(req.GetAuthInfo())
	if err != nil {
		return &pb.AppendDatasetSyncPointRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	rsp, err := markerNode.AppendDatasetSyncPointMarker(ctx, &pb.AppendDatasetSyncPointMarkerReq{AuthInfo: auth, SpaceId: req.GetSpaceId(), SyncPoint: marker})
	if err != nil {
		return &pb.AppendDatasetSyncPointRsp{RetInfo: markerError(err)}, nil
	}
	return &pb.AppendDatasetSyncPointRsp{RetInfo: rsp.GetRetInfo(), EventId: rsp.GetEventId()}, nil
}

func (s *Service) GetFactorPeriodComputed(ctx context.Context, req *pb.GetFactorPeriodComputedReq) (*pb.GetFactorPeriodComputedRsp, error) {
	if req == nil || req.GetSpaceId() == "" || req.GetSourceViewId() == "" || req.GetTriggerEventId() == "" || req.GetPeriodTime() <= 0 {
		return &pb.GetFactorPeriodComputedRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, source_view_id, trigger_event_id and period_time are required"))}, nil
	}
	if err := s.authorizeRequest(req.GetAuthInfo()); err != nil {
		return &pb.GetFactorPeriodComputedRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	if s.result == nil {
		return &pb.GetFactorPeriodComputedRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errors.New("factor result dataset resolver is unavailable"))}, nil
	}
	resultDatasetID, err := s.result(ctx, req.GetSpaceId(), req.GetSourceViewId())
	if err != nil {
		return &pb.GetFactorPeriodComputedRsp{RetInfo: markerError(err)}, nil
	}
	ctx = s.requestContext(ctx)
	node, err := s.resolve(ctx, req.GetSpaceId(), resultDatasetID)
	if err != nil {
		return &pb.GetFactorPeriodComputedRsp{RetInfo: markerError(err)}, nil
	}
	markerNode, ok := node.(markerDataNodeClient)
	if !ok {
		return &pb.GetFactorPeriodComputedRsp{RetInfo: markerError(errors.New("DataNode marker RPC is unavailable"))}, nil
	}
	auth, err := s.signAuth(req.GetAuthInfo())
	if err != nil {
		return &pb.GetFactorPeriodComputedRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	rsp, err := markerNode.GetFactorPeriodComputedMarker(ctx, &pb.GetFactorPeriodComputedMarkerReq{
		AuthInfo: auth, SpaceId: req.GetSpaceId(), ResultDatasetId: resultDatasetID, SourceViewId: req.GetSourceViewId(),
		TriggerEventId: req.GetTriggerEventId(), PeriodTime: req.GetPeriodTime(),
	})
	if err != nil {
		return &pb.GetFactorPeriodComputedRsp{RetInfo: markerError(err)}, nil
	}
	return &pb.GetFactorPeriodComputedRsp{RetInfo: rsp.GetRetInfo(), Found: rsp.GetFound(), EventId: rsp.GetEventId(), Marker: rsp.GetMarker()}, nil
}

func (s *Service) WaitViewSyncPoint(ctx context.Context, req *pb.WaitViewSyncPointReq) (*pb.WaitViewSyncPointRsp, error) {
	if req == nil || req.GetSpaceId() == "" || req.GetViewId() == "" || req.GetRequestId() == "" || len(req.GetDatasetIds()) == 0 {
		return &pb.WaitViewSyncPointRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, view_id, request_id and dataset_ids are required"))}, nil
	}
	if err := s.authorizeRequest(req.GetAuthInfo()); err != nil {
		return &pb.WaitViewSyncPointRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	if s.syncPoint == nil {
		return &pb.WaitViewSyncPointRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errors.New("view sync point reader is unavailable"))}, nil
	}
	datasetIDs := uniqueSorted(req.GetDatasetIds())
	wait := time.Duration(req.GetWaitTimeoutMs()) * time.Millisecond
	if wait > 30*time.Second {
		wait = 30 * time.Second
	}
	deadline := time.Now().Add(wait)
	for {
		missing, err := s.syncPoint.MissingViewSyncPointDatasets(ctx, req.GetSpaceId(), req.GetViewId(), req.GetRequestId(), datasetIDs)
		if err != nil {
			return &pb.WaitViewSyncPointRsp{RetInfo: markerError(err)}, nil
		}
		if len(missing) == 0 {
			return &pb.WaitViewSyncPointRsp{RetInfo: retinfo.Success("success"), Ready: true}, nil
		}
		if wait == 0 || !time.Now().Before(deadline) {
			return &pb.WaitViewSyncPointRsp{RetInfo: retinfo.Success("pending"), PendingDatasetIds: missing}, nil
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return &pb.WaitViewSyncPointRsp{RetInfo: markerError(ctx.Err()), PendingDatasetIds: missing}, nil
		case <-timer.C:
		}
	}
}

func (s *Service) validateMarkerCaller(auth *pb.AuthInfo, spaceID, datasetID, owner string) error {
	if spaceID == "" || datasetID == "" {
		return markerValidationError{errors.New("space_id and dataset_id are required")}
	}
	if err := s.authorizeRequest(auth); err != nil {
		return markerPermissionError{err}
	}
	appID := strings.ToLower(strings.TrimSpace(auth.GetAppId()))
	if owner != "" && appID != owner && appID != "moox-"+owner {
		return markerPermissionError{fmt.Errorf("%s marker requires %s caller", owner, owner)}
	}
	return nil
}

type markerValidationError struct{ error }
type markerPermissionError struct{ error }

func markerError(err error) *pb.RetInfo {
	var validation markerValidationError
	if errors.As(err, &validation) {
		return retinfo.Error(pb.ErrorCode_INVALID_PARAM, validation)
	}
	var permission markerPermissionError
	if errors.As(err, &permission) {
		return retinfo.Error(pb.ErrorCode_NO_PERMISSION, permission)
	}
	return retinfo.Error(pb.ErrorCode_INNER_ERR, err)
}

func uniqueSorted(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			set[value] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
