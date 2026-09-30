package datanode

import (
	"context"
	"errors"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func (s *Service) EnsureDatasetPeriod(ctx context.Context, req *pb.EnsureDatasetPeriodReq) (*pb.EnsureDatasetPeriodRsp, error) {
	if req == nil || req.GetExpectation() == nil {
		return &pb.EnsureDatasetPeriodRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("expectation is required"))}, nil
	}
	if req.GetNodeId() != "" && req.GetNodeId() != s.nodeID {
		return &pb.EnsureDatasetPeriodRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("node_id does not match DataNode"))}, nil
	}
	if err := s.validateAuth(req.GetAuthInfo()); err != nil {
		return &pb.EnsureDatasetPeriodRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	status, err := s.store.EnsureDatasetPeriod(ctx, periodExpectation(req.GetExpectation()))
	if err != nil {
		return &pb.EnsureDatasetPeriodRsp{RetInfo: retinfo.Error(errorCode(err), err)}, nil
	}
	return &pb.EnsureDatasetPeriodRsp{RetInfo: retinfo.Success("success"), Status: status}, nil
}

func (s *Service) CommitTimeSeriesBatch(ctx context.Context, req *pb.CommitTimeSeriesBatchReq) (*pb.CommitTimeSeriesBatchRsp, error) {
	if req == nil || req.GetExpectation() == nil || len(req.GetItems()) == 0 {
		return &pb.CommitTimeSeriesBatchRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("expectation and items are required"))}, nil
	}
	if req.GetNodeId() != "" && req.GetNodeId() != s.nodeID {
		return &pb.CommitTimeSeriesBatchRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("node_id does not match DataNode"))}, nil
	}
	if err := s.validateAuth(req.GetAuthInfo()); err != nil {
		return &pb.CommitTimeSeriesBatchRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	items := make([]pebble.TimeSeriesBatchItem, 0, len(req.GetItems()))
	keys := make([]*pb.RowKey, 0, len(req.GetItems()))
	for _, item := range req.GetItems() {
		if item == nil || item.GetRow() == nil {
			return &pb.CommitTimeSeriesBatchRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("batch item row is required"))}, nil
		}
		items = append(items, pebble.TimeSeriesBatchItem{SeriesIndex: item.GetSeriesIndex(), Row: item.GetRow()})
		keys = append(keys, item.GetRow().GetKey())
	}
	status, err := s.store.CommitTimeSeriesBatch(ctx, periodExpectation(req.GetExpectation()), items, req.GetSourceEventId(), req.GetWriteSource())
	if err != nil {
		return &pb.CommitTimeSeriesBatchRsp{RetInfo: retinfo.Error(errorCode(err), err)}, nil
	}
	return &pb.CommitTimeSeriesBatchRsp{RetInfo: retinfo.Success("success"), Keys: keys, PeriodStatus: status}, nil
}

func (s *Service) RecordDatasetPeriodFailures(ctx context.Context, req *pb.RecordDatasetPeriodFailuresReq) (*pb.RecordDatasetPeriodFailuresRsp, error) {
	if req == nil || req.GetExpectation() == nil || len(req.GetSeriesIndexes()) == 0 {
		return &pb.RecordDatasetPeriodFailuresRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("expectation and series_indexes are required"))}, nil
	}
	if req.GetNodeId() != "" && req.GetNodeId() != s.nodeID {
		return &pb.RecordDatasetPeriodFailuresRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("node_id does not match DataNode"))}, nil
	}
	if err := s.validateAuth(req.GetAuthInfo()); err != nil {
		return &pb.RecordDatasetPeriodFailuresRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	status, err := s.store.RecordDatasetPeriodFailures(ctx, periodExpectation(req.GetExpectation()), req.GetSeriesIndexes())
	if err != nil {
		return &pb.RecordDatasetPeriodFailuresRsp{RetInfo: retinfo.Error(errorCode(err), err)}, nil
	}
	return &pb.RecordDatasetPeriodFailuresRsp{RetInfo: retinfo.Success("success"), PeriodStatus: status}, nil
}

func periodExpectation(in *pb.DatasetPeriodExpectation) pebble.DatasetPeriodExpectation {
	if in == nil {
		return pebble.DatasetPeriodExpectation{}
	}
	roster := make([]pebble.DatasetPeriodSeries, 0, len(in.GetRoster()))
	for _, row := range in.GetRoster() {
		if row == nil {
			continue
		}
		roster = append(roster, pebble.DatasetPeriodSeries{SeriesIndex: row.GetSeriesIndex(), SubjectID: row.GetSubjectId()})
	}
	return pebble.DatasetPeriodExpectation{
		SpaceID: in.GetSpaceId(), DatasetID: in.GetDatasetId(), Frequency: in.GetFrequency(), PeriodTime: in.GetPeriodTime(),
		SeriesHash: in.GetSeriesHash(), ExpectedCount: in.GetExpectedCount(), DeadlineAt: in.GetDeadlineAt(), Roster: roster,
	}
}
