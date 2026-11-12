package primarystore

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	storageeventpb "github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/proto"
)

// ReportDatasetPeriodCompleted 追加写入方自己报告的周期完成（source=producer）；账本判定的完成由存储层在周期封口时生成。
func (s *Service) ReportDatasetPeriodCompleted(ctx context.Context, req *pb.ReportDatasetPeriodCompletedReq) (*pb.ReportDatasetPeriodCompletedRsp, error) {
	if err := s.validateMarkerCaller(req.GetAuthInfo(), req.GetSpaceId(), req.GetMarker().GetDatasetId()); err != nil {
		return &pb.ReportDatasetPeriodCompletedRsp{RetInfo: markerError(err)}, nil
	}
	ctx = s.requestContext(ctx)
	if err := requireActiveDataset(ctx, req.GetSpaceId(), req.GetMarker().GetDatasetId()); err != nil {
		return &pb.ReportDatasetPeriodCompletedRsp{RetInfo: markerError(err)}, nil
	}
	raw, _, err := pebble.BuildProducerPeriodCompletedMessage(req.GetSpaceId(), req.GetMarker())
	if err != nil {
		return &pb.ReportDatasetPeriodCompletedRsp{RetInfo: storeError(err)}, nil
	}
	eventID, err := s.store.AppendDatasetMarker(ctx, raw)
	if err != nil {
		return &pb.ReportDatasetPeriodCompletedRsp{RetInfo: storeError(err)}, nil
	}
	return &pb.ReportDatasetPeriodCompletedRsp{RetInfo: retinfo.Success("success"), EventId: eventID}, nil
}

func (s *Service) AppendDatasetSyncPoint(ctx context.Context, req *pb.AppendDatasetSyncPointReq) (*pb.AppendDatasetSyncPointRsp, error) {
	marker := req.GetSyncPoint()
	if err := s.validateMarkerCaller(req.GetAuthInfo(), req.GetSpaceId(), marker.GetDatasetId()); err != nil {
		return &pb.AppendDatasetSyncPointRsp{RetInfo: markerError(err)}, nil
	}
	if marker.GetRequestId() == "" || (marker.GetSource() != "import" && marker.GetSource() != "catchup") {
		return &pb.AppendDatasetSyncPointRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("request_id and source import|catchup are required"))}, nil
	}
	ctx = s.requestContext(ctx)
	if err := requireActiveDataset(ctx, req.GetSpaceId(), marker.GetDatasetId()); err != nil {
		return &pb.AppendDatasetSyncPointRsp{RetInfo: markerError(err)}, nil
	}
	raw, _, err := pebble.BuildDatasetSyncPointMessage(req.GetSpaceId(), marker)
	if err != nil {
		return &pb.AppendDatasetSyncPointRsp{RetInfo: storeError(err)}, nil
	}
	eventID, err := s.store.AppendDatasetMarker(ctx, raw)
	if err != nil {
		return &pb.AppendDatasetSyncPointRsp{RetInfo: storeError(err)}, nil
	}
	return &pb.AppendDatasetSyncPointRsp{RetInfo: retinfo.Success("success"), EventId: eventID}, nil
}

func (s *Service) GetDatasetPeriodCompleted(ctx context.Context, req *pb.GetDatasetPeriodCompletedReq) (*pb.GetDatasetPeriodCompletedRsp, error) {
	if req == nil || req.GetSpaceId() == "" || req.GetDatasetId() == "" || req.GetCauseEventId() == "" || req.GetPeriodTime() <= 0 {
		return &pb.GetDatasetPeriodCompletedRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, dataset_id, cause_event_id and period_time are required"))}, nil
	}
	if err := s.authorizeRequest(req.GetAuthInfo()); err != nil {
		return &pb.GetDatasetPeriodCompletedRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	ctx = s.requestContext(ctx)
	if err := requireActiveDataset(ctx, req.GetSpaceId(), req.GetDatasetId()); err != nil {
		return &pb.GetDatasetPeriodCompletedRsp{RetInfo: markerError(err)}, nil
	}
	message, found, err := s.store.GetDatasetPeriodCompletedMarker(ctx, req.GetSpaceId(), req.GetDatasetId(), req.GetCauseEventId(), req.GetPeriodTime())
	if err != nil {
		return &pb.GetDatasetPeriodCompletedRsp{RetInfo: storeError(err)}, nil
	}
	if !found {
		return &pb.GetDatasetPeriodCompletedRsp{RetInfo: retinfo.Success("success")}, nil
	}
	payload := &storageeventpb.DatasetPeriodCompleted{}
	if err := proto.Unmarshal(message.GetPayload(), payload); err != nil {
		return &pb.GetDatasetPeriodCompletedRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	return &pb.GetDatasetPeriodCompletedRsp{
		RetInfo: retinfo.Success("success"), Found: true, EventId: message.GetEventId(),
		Marker: &pb.DatasetPeriodCompletedMarker{
			DatasetId: payload.GetDatasetId(), Frequency: payload.GetFrequency(), PeriodTime: payload.GetPeriodTime(),
			Status: payload.GetStatus(), UniverseSubjectIds: append([]string(nil), payload.GetUniverseSubjectIds()...),
			FailedSubjects: append([]string(nil), payload.GetFailedSubjects()...), CauseEventId: payload.GetCauseEventId(),
			UniverseSeries: markerSeries(payload.GetUniverseSeries()), FailedSeries: markerSeries(payload.GetFailedSeries()),
			CompletedAt: payload.GetCompletedAt(), DetailType: payload.GetDetailType(), Detail: append([]byte(nil), payload.GetDetail()...),
		},
	}, nil
}

// markerSeries 把事件形态的序列名单转成 Storage RPC 形态。
func markerSeries(values []*storageeventpb.SeriesRef) []*pb.SeriesRef {
	out := make([]*pb.SeriesRef, 0, len(values))
	for _, value := range values {
		out = append(out, &pb.SeriesRef{SubjectId: value.GetSubjectId(), SeriesTag: value.GetSeriesTag()})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// storeError 把存储层错误转成带错误码的返回信息。
func storeError(err error) *pb.RetInfo {
	return retinfo.Error(pebble.ErrorCode(err), err)
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

func (s *Service) validateMarkerCaller(auth *pb.AuthInfo, spaceID, datasetID string) error {
	if spaceID == "" || datasetID == "" {
		return markerValidationError{errors.New("space_id and dataset_id are required")}
	}
	if err := s.authorizeRequest(auth); err != nil {
		return markerPermissionError{err}
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
