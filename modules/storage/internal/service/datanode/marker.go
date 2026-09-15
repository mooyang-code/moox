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

func (s *Service) AppendCollectorPeriodCompleted(ctx context.Context, req *pb.AppendCollectorPeriodCompletedReq) (*pb.AppendCollectorPeriodCompletedRsp, error) {
	if err := s.validateMarkerRequest(req.GetNodeId(), req.GetSpaceId(), req.GetAuthInfo()); err != nil {
		return &pb.AppendCollectorPeriodCompletedRsp{RetInfo: markerRetInfo(err)}, nil
	}
	raw, _, err := pebble.BuildCollectorPeriodCompletedMessage(req.GetSpaceId(), req.GetMarker())
	if err == nil {
		var eventID string
		eventID, err = s.store.AppendDatasetMarker(ctx, raw)
		if err == nil {
			return &pb.AppendCollectorPeriodCompletedRsp{RetInfo: retinfo.Success("success"), EventId: eventID}, nil
		}
	}
	return &pb.AppendCollectorPeriodCompletedRsp{RetInfo: retinfo.Error(errorCode(err), err)}, nil
}

func (s *Service) AppendMergePeriodCompleted(ctx context.Context, req *pb.AppendMergePeriodCompletedReq) (*pb.AppendMergePeriodCompletedRsp, error) {
	if err := s.validateMarkerRequest(req.GetNodeId(), req.GetSpaceId(), req.GetAuthInfo()); err != nil {
		return &pb.AppendMergePeriodCompletedRsp{RetInfo: markerRetInfo(err)}, nil
	}
	raw, _, err := pebble.BuildMergePeriodCompletedMessage(req.GetSpaceId(), req.GetMarker())
	if err == nil {
		var eventID string
		eventID, err = s.store.AppendDatasetMarker(ctx, raw)
		if err == nil {
			return &pb.AppendMergePeriodCompletedRsp{RetInfo: retinfo.Success("success"), EventId: eventID}, nil
		}
	}
	return &pb.AppendMergePeriodCompletedRsp{RetInfo: retinfo.Error(errorCode(err), err)}, nil
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
	message, found, err := s.store.GetFactorPeriodComputedMarker(ctx, req.GetSpaceId(), req.GetDatasetId(), req.GetTriggerEventId(), req.GetPeriodTime())
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
			DatasetId: payload.GetDatasetId(), Frequency: payload.GetFrequency(), PeriodTime: payload.GetPeriodTime(),
			Status: payload.GetStatus(), BatchId: payload.GetBatchId(), ConfigSnapshotId: payload.GetConfigSnapshotId(),
			ExpectedScopeRef: payload.GetExpectedScopeRef(), UniverseSubjectIds: append([]string(nil), payload.GetUniverseSubjectIds()...),
			Bindings: bindings, CommittedPositions: markerPositions(payload.GetCommittedPositions()),
			ComputedAt: payload.GetComputedAt(), TriggerEventId: payload.GetTriggerEventId(),
		},
	}, nil
}

func markerPositions(values []*storageeventpb.CommittedPosition) []*pb.CommittedPosition {
	out := make([]*pb.CommittedPosition, 0, len(values))
	for _, value := range values {
		if value == nil {
			continue
		}
		out = append(out, &pb.CommittedPosition{NodeId: value.GetNodeId(), StoreId: value.GetStoreId(), Sequence: value.GetSequence()})
	}
	return out
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
