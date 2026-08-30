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

func (s *Service) AppendDatasetPeriodCollected(ctx context.Context, req *pb.AppendDatasetPeriodCollectedReq) (*pb.AppendDatasetPeriodCollectedRsp, error) {
	if err := s.validateMarkerRequest(req.GetNodeId(), req.GetSpaceId(), req.GetAuthInfo()); err != nil {
		return &pb.AppendDatasetPeriodCollectedRsp{RetInfo: markerRetInfo(err)}, nil
	}
	raw, _, err := pebble.BuildDatasetPeriodCollectedMessage(req.GetSpaceId(), req.GetMarker())
	if err == nil {
		var eventID string
		eventID, err = s.store.AppendDatasetMarker(ctx, raw)
		if err == nil {
			return &pb.AppendDatasetPeriodCollectedRsp{RetInfo: retinfo.Success("success"), EventId: eventID}, nil
		}
	}
	return &pb.AppendDatasetPeriodCollectedRsp{RetInfo: retinfo.Error(errorCode(err), err)}, nil
}

func (s *Service) AppendFactorPeriodComputed(ctx context.Context, req *pb.AppendFactorPeriodComputedReq) (*pb.AppendFactorPeriodComputedRsp, error) {
	if err := s.validateMarkerRequest(req.GetNodeId(), req.GetSpaceId(), req.GetAuthInfo()); err != nil {
		return &pb.AppendFactorPeriodComputedRsp{RetInfo: markerRetInfo(err)}, nil
	}
	raw, _, err := pebble.BuildFactorPeriodComputedMessage(req.GetSpaceId(), req.GetMarker())
	if err == nil {
		var eventID string
		eventID, err = s.store.AppendDatasetMarker(ctx, raw)
		if err == nil {
			return &pb.AppendFactorPeriodComputedRsp{RetInfo: retinfo.Success("success"), EventId: eventID}, nil
		}
	}
	return &pb.AppendFactorPeriodComputedRsp{RetInfo: retinfo.Error(errorCode(err), err)}, nil
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

func (s *Service) GetFactorPeriodComputedMarker(ctx context.Context, req *pb.GetFactorPeriodComputedMarkerReq) (*pb.GetFactorPeriodComputedMarkerRsp, error) {
	if err := s.validateMarkerRequest(req.GetNodeId(), req.GetSpaceId(), req.GetAuthInfo()); err != nil {
		return &pb.GetFactorPeriodComputedMarkerRsp{RetInfo: markerRetInfo(err)}, nil
	}
	message, found, err := s.store.GetFactorPeriodComputedMarker(ctx, req.GetSpaceId(), req.GetResultDatasetId(), req.GetSourceViewId(), req.GetTriggerEventId(), req.GetPeriodTime())
	if err != nil {
		return &pb.GetFactorPeriodComputedMarkerRsp{RetInfo: retinfo.Error(errorCode(err), err)}, nil
	}
	if !found {
		return &pb.GetFactorPeriodComputedMarkerRsp{RetInfo: retinfo.Success("success")}, nil
	}
	payload := &storageeventpb.FactorPeriodComputed{}
	if err := proto.Unmarshal(message.GetPayload(), payload); err != nil {
		return &pb.GetFactorPeriodComputedMarkerRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	bindings := make([]*pb.FactorBindingPeriodState, 0, len(payload.GetBindings()))
	for _, state := range payload.GetBindings() {
		if state == nil {
			continue
		}
		bindings = append(bindings, &pb.FactorBindingPeriodState{
			BindingId: state.GetBindingId(), FactorId: state.GetFactorId(), Status: state.GetStatus(),
			SkippedSubjects: append([]string(nil), state.GetSkippedSubjects()...), FailedSubjects: append([]string(nil), state.GetFailedSubjects()...),
			SourceHash: state.GetSourceHash(),
		})
	}
	return &pb.GetFactorPeriodComputedMarkerRsp{
		RetInfo: retinfo.Success("success"), Found: true, EventId: message.GetEventId(),
		Marker: &pb.FactorPeriodComputedMarker{
			SourceViewId: payload.GetSourceViewId(), ResultDatasetId: payload.GetResultDatasetId(), Frequency: payload.GetFrequency(),
			PeriodTime: payload.GetPeriodTime(), Status: payload.GetStatus(), Bindings: bindings,
			ComputedAt: payload.GetComputedAt(), TriggerEventId: payload.GetTriggerEventId(), SourceIndexId: payload.GetSourceIndexId(), SourceIndexRevision: payload.GetSourceIndexRevision(),
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
