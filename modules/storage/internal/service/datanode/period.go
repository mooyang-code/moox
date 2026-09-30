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
	result, err := s.store.EnsureDatasetPeriod(ctx, periodExpectation(req.GetExpectation()))
	if err != nil {
		return &pb.EnsureDatasetPeriodRsp{RetInfo: retinfo.Error(errorCode(err), err)}, nil
	}
	return &pb.EnsureDatasetPeriodRsp{RetInfo: retinfo.Success("success"), Status: result.Status, DeadlineAt: result.DeadlineAt}, nil
}

func (s *Service) GetDatasetPeriodStatus(ctx context.Context, req *pb.GetDatasetPeriodStatusReq) (*pb.GetDatasetPeriodStatusRsp, error) {
	if req == nil || req.GetExpectation() == nil {
		return &pb.GetDatasetPeriodStatusRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("expectation is required"))}, nil
	}
	if req.GetNodeId() != "" && req.GetNodeId() != s.nodeID {
		return &pb.GetDatasetPeriodStatusRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("node_id does not match DataNode"))}, nil
	}
	if err := s.validateAuth(req.GetAuthInfo()); err != nil {
		return &pb.GetDatasetPeriodStatusRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	progress, err := s.store.GetDatasetPeriodStatus(ctx, periodExpectation(req.GetExpectation()))
	if err != nil {
		return &pb.GetDatasetPeriodStatusRsp{RetInfo: retinfo.Error(errorCode(err), err)}, nil
	}
	return &pb.GetDatasetPeriodStatusRsp{RetInfo: retinfo.Success("success"), Status: progress.Status, SeriesHash: progress.SeriesHash, ExpectedCount: progress.ExpectedCount, DeadlineAt: progress.DeadlineAt}, nil
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
	result, err := s.store.CommitTimeSeriesBatch(ctx, periodExpectation(req.GetExpectation()), items, req.GetSourceEventId(), req.GetWriteSource())
	if err != nil {
		return &pb.CommitTimeSeriesBatchRsp{RetInfo: retinfo.Error(errorCode(err), err)}, nil
	}
	return &pb.CommitTimeSeriesBatchRsp{RetInfo: retinfo.Success("success"), Keys: keys, PeriodStatus: result.Status, AcceptedSeriesIndexes: result.AcceptedSeriesIndexes}, nil
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
	result, err := s.store.RecordDatasetPeriodFailures(ctx, periodExpectation(req.GetExpectation()), req.GetSeriesIndexes())
	if err != nil {
		return &pb.RecordDatasetPeriodFailuresRsp{RetInfo: retinfo.Error(errorCode(err), err)}, nil
	}
	return &pb.RecordDatasetPeriodFailuresRsp{RetInfo: retinfo.Success("success"), PeriodStatus: result.Status, Results: result.FailureResults}, nil
}

func periodExpectation(in *pb.DatasetPeriodExpectation) pebble.DatasetPeriodExpectation {
	if in == nil {
		return pebble.DatasetPeriodExpectation{}
	}
	seriesSnapshot := make([]pebble.DatasetPeriodSeries, len(in.GetSeriesSnapshot()))
	for index, row := range in.GetSeriesSnapshot() {
		seriesSnapshot[index] = pebble.DatasetPeriodSeries{SeriesIndex: row.GetSeriesIndex(), SubjectID: row.GetSubjectId(), SeriesTag: row.GetSeriesTag()}
	}
	return pebble.DatasetPeriodExpectation{
		SpaceID: in.GetSpaceId(), DatasetID: in.GetDatasetId(), Frequency: in.GetFrequency(), PeriodTime: in.GetPeriodTime(),
		SeriesHash: in.GetSeriesHash(), ExpectedCount: in.GetExpectedCount(), DeadlineAt: in.GetDeadlineAt(), SeriesSnapshot: seriesSnapshot,
	}
}
