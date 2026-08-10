package catalog

import (
	"context"
	"errors"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func (s *Service) UpsertViewPeriodDatasetState(ctx context.Context, req *pb.UpsertViewPeriodDatasetStateReq) (*pb.UpsertViewPeriodDatasetStateRsp, error) {
	if req == nil || req.GetState() == nil {
		return &pb.UpsertViewPeriodDatasetStateRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("state is required"))}, nil
	}
	state, err := s.metadata.UpsertViewPeriodDatasetState(ctx, req.GetState())
	if err != nil {
		return &pb.UpsertViewPeriodDatasetStateRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.UpsertViewPeriodDatasetStateRsp{RetInfo: retinfo.Success("success"), State: state}, nil
}

func (s *Service) ListViewPeriodDatasetStates(ctx context.Context, req *pb.ListViewPeriodDatasetStatesReq) (*pb.ListViewPeriodDatasetStatesRsp, error) {
	if req == nil {
		return &pb.ListViewPeriodDatasetStatesRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("request is required"))}, nil
	}
	states, err := s.metadata.ListViewPeriodDatasetStates(ctx, req.GetSpaceId(), req.GetViewId(), req.GetFrequency(), req.GetPeriodTime())
	if err != nil {
		return &pb.ListViewPeriodDatasetStatesRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.ListViewPeriodDatasetStatesRsp{RetInfo: retinfo.Success("success"), States: states}, nil
}

func (s *Service) RecordViewSyncPoint(ctx context.Context, req *pb.RecordViewSyncPointReq) (*pb.RecordViewSyncPointRsp, error) {
	if req == nil || req.GetSyncPoint() == nil {
		return &pb.RecordViewSyncPointRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("sync_point is required"))}, nil
	}
	syncPoint, err := s.metadata.RecordViewSyncPoint(ctx, req.GetSyncPoint())
	if err != nil {
		return &pb.RecordViewSyncPointRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.RecordViewSyncPointRsp{RetInfo: retinfo.Success("success"), SyncPoint: syncPoint}, nil
}
