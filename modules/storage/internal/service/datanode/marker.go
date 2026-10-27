package datanode

import (
	"context"
	"errors"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	storageeventpb "github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/proto"
)

// AppendDatasetPeriodCompleted 追加写入方报告的周期完成（source=producer）。账本判定的完成由 DataNode 自己在周期封口时生成。
func (s *Service) AppendDatasetPeriodCompleted(ctx context.Context, req *pb.AppendDatasetPeriodCompletedReq) (*pb.AppendDatasetPeriodCompletedRsp, error) {
	if err := s.validateMarkerRequest(req.GetNodeId(), req.GetSpaceId(), req.GetAuthInfo()); err != nil {
		return &pb.AppendDatasetPeriodCompletedRsp{RetInfo: markerRetInfo(err)}, nil
	}
	raw, _, err := pebble.BuildProducerPeriodCompletedMessage(req.GetSpaceId(), req.GetMarker())
	if err == nil {
		var eventID string
		eventID, err = s.store.AppendDatasetMarker(ctx, raw)
		if err == nil {
			return &pb.AppendDatasetPeriodCompletedRsp{RetInfo: retinfo.Success("success"), EventId: eventID}, nil
		}
	}
	return &pb.AppendDatasetPeriodCompletedRsp{RetInfo: retinfo.Error(errorCode(err), err)}, nil
}

func (s *Service) AppendDatasetSyncPointMarker(ctx context.Context, req *pb.AppendDatasetSyncPointMarkerReq) (*pb.AppendDatasetSyncPointMarkerRsp, error) {
	if err := s.validateMarkerRequest(req.GetNodeId(), req.GetSpaceId(), req.GetAuthInfo()); err != nil {
		return &pb.AppendDatasetSyncPointMarkerRsp{RetInfo: markerRetInfo(err)}, nil
	}
	raw, _, err := pebble.BuildDatasetSyncPointMessage(req.GetSpaceId(), req.GetSyncPoint())
	if err == nil {
		var eventID string
		eventID, err = s.store.AppendDatasetMarker(ctx, raw)
		if err == nil {
			return &pb.AppendDatasetSyncPointMarkerRsp{RetInfo: retinfo.Success("success"), EventId: eventID}, nil
		}
	}
	return &pb.AppendDatasetSyncPointMarkerRsp{RetInfo: retinfo.Error(errorCode(err), err)}, nil
}

func (s *Service) GetDatasetPeriodCompletedMarker(ctx context.Context, req *pb.GetDatasetPeriodCompletedMarkerReq) (*pb.GetDatasetPeriodCompletedMarkerRsp, error) {
	if err := s.validateMarkerRequest(req.GetNodeId(), req.GetSpaceId(), req.GetAuthInfo()); err != nil {
		return &pb.GetDatasetPeriodCompletedMarkerRsp{RetInfo: markerRetInfo(err)}, nil
	}
	message, found, err := s.store.GetDatasetPeriodCompletedMarker(ctx, req.GetSpaceId(), req.GetDatasetId(), req.GetCauseEventId(), req.GetPeriodTime())
	if err != nil {
		return &pb.GetDatasetPeriodCompletedMarkerRsp{RetInfo: retinfo.Error(errorCode(err), err)}, nil
	}
	if !found {
		return &pb.GetDatasetPeriodCompletedMarkerRsp{RetInfo: retinfo.Success("success")}, nil
	}
	payload := &storageeventpb.DatasetPeriodCompleted{}
	if err := proto.Unmarshal(message.GetPayload(), payload); err != nil {
		return &pb.GetDatasetPeriodCompletedMarkerRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	return &pb.GetDatasetPeriodCompletedMarkerRsp{
		RetInfo: retinfo.Success("success"), Found: true, EventId: message.GetEventId(),
		Marker: &pb.DatasetPeriodCompletedMarker{
			DatasetId: payload.GetDatasetId(), Frequency: payload.GetFrequency(), PeriodTime: payload.GetPeriodTime(),
			Status: payload.GetStatus(), UniverseSubjectIds: append([]string(nil), payload.GetUniverseSubjectIds()...),
			FailedSubjects: append([]string(nil), payload.GetFailedSubjects()...), CauseEventId: payload.GetCauseEventId(),
			CompletedAt: payload.GetCompletedAt(), DetailType: payload.GetDetailType(), Detail: append([]byte(nil), payload.GetDetail()...),
		},
	}, nil
}

func (s *Service) validateMarkerRequest(nodeID, spaceID string, auth *pb.AuthInfo) error {
	if s == nil || s.store == nil {
		return errors.New("DataNode is not initialized")
	}
	if spaceID == "" {
		return pebble.ValidationErrorFor("space_id is required")
	}
	if nodeID != "" && nodeID != s.nodeID {
		return pebble.ValidationErrorFor("node_id does not match DataNode")
	}
	if err := s.validateAuth(auth); err != nil {
		return markerPermissionError{err}
	}
	return nil
}

type markerPermissionError struct{ error }

func markerRetInfo(err error) *pb.RetInfo {
	var permission markerPermissionError
	if errors.As(err, &permission) {
		return retinfo.Error(pb.ErrorCode_NO_PERMISSION, permission)
	}
	return retinfo.Error(errorCode(err), err)
}
