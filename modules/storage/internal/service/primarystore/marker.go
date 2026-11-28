package primarystore

import (
	"context"
	"errors"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	storageeventpb "github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/proto"
)

// ReportDatasetPeriodCompleted 追加写入方自己报告的周期完成（source=producer）；账本判定的完成由存储层在周期封口时生成。
func (s *Service) ReportDatasetPeriodCompleted(ctx context.Context, req *pb.ReportDatasetPeriodCompletedReq) (*pb.ReportDatasetPeriodCompletedRsp, error) {
	if err := validateMarkerScope(req.GetSpaceId(), req.GetMarker().GetDatasetId()); err != nil {
		return &pb.ReportDatasetPeriodCompletedRsp{RetInfo: markerError(err)}, nil
	}
	ctx = s.requestContext(ctx)
	if err := requireActiveDataset(ctx, req.GetSpaceId(), req.GetMarker().GetDatasetId()); err != nil {
		return &pb.ReportDatasetPeriodCompletedRsp{RetInfo: markerError(err)}, nil
	}
	raw, _, err := pebble.BuildProducerPeriodCompletedMessage(req.GetSpaceId(), req.GetMarker())
	if err != nil {
		return &pb.ReportDatasetPeriodCompletedRsp{RetInfo: retinfo.FromError(err)}, nil
	}
	eventID, err := s.store.AppendDatasetMarker(ctx, raw)
	if err != nil {
		return &pb.ReportDatasetPeriodCompletedRsp{RetInfo: retinfo.FromError(err)}, nil
	}
	return &pb.ReportDatasetPeriodCompletedRsp{RetInfo: retinfo.Success("success"), EventId: eventID}, nil
}

func (s *Service) GetDatasetPeriodCompleted(ctx context.Context, req *pb.GetDatasetPeriodCompletedReq) (*pb.GetDatasetPeriodCompletedRsp, error) {
	if req == nil || req.GetSpaceId() == "" || req.GetDatasetId() == "" || req.GetCauseEventId() == "" || req.GetPeriodTime() <= 0 {
		return &pb.GetDatasetPeriodCompletedRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, dataset_id, cause_event_id and period_time are required"))}, nil
	}
	ctx = s.requestContext(ctx)
	if err := requireActiveDataset(ctx, req.GetSpaceId(), req.GetDatasetId()); err != nil {
		return &pb.GetDatasetPeriodCompletedRsp{RetInfo: markerError(err)}, nil
	}
	message, found, err := s.store.GetDatasetPeriodCompletedMarker(ctx, req.GetSpaceId(), req.GetDatasetId(), req.GetCauseEventId(), req.GetPeriodTime())
	if err != nil {
		return &pb.GetDatasetPeriodCompletedRsp{RetInfo: retinfo.FromError(err)}, nil
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
			CompletedAt: payload.GetCompletedAt(), DetailType: payload.GetDetailType(), Detail: append([]byte(nil), payload.GetDetail()...),
		},
	}, nil
}

func validateMarkerScope(spaceID, datasetID string) error {
	if spaceID == "" || datasetID == "" {
		return markerValidationError{errors.New("space_id and dataset_id are required")}
	}
	return nil
}

type markerValidationError struct{ error }

func markerError(err error) *pb.RetInfo {
	var validation markerValidationError
	if errors.As(err, &validation) {
		return retinfo.Error(pb.ErrorCode_INVALID_PARAM, validation)
	}
	return retinfo.Error(pb.ErrorCode_INNER_ERR, err)
}
