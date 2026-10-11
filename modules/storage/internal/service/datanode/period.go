package datanode

import (
	"context"
	"errors"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/seriesset"
)

func (s *Service) RegisterSeriesSnapshot(ctx context.Context, req *pb.RegisterSeriesSnapshotReq) (*pb.RegisterSeriesSnapshotRsp, error) {
	if req == nil || len(req.GetSeries()) == 0 {
		return &pb.RegisterSeriesSnapshotRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("series snapshot is required"))}, nil
	}
	if err := validatePeriodNodeID(req.GetNodeId(), s.nodeID); err != nil {
		return &pb.RegisterSeriesSnapshotRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	if err := s.validateAuth(req.GetAuthInfo()); err != nil {
		return &pb.RegisterSeriesSnapshotRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	series := make([]seriesset.Series, len(req.GetSeries()))
	for i, entry := range req.GetSeries() {
		series[i] = seriesset.Series{Provider: entry.GetProvider(), SourceID: entry.GetSourceId(), MarketType: entry.GetMarketType(), SubjectID: entry.GetSubjectId(), SeriesTag: entry.GetSeriesTag()}
	}
	result, err := s.store.RegisterSeriesSnapshot(ctx, req.GetSpaceId(), req.GetDatasetId(), req.GetFrequency(), series, req.GetEffectiveFrom())
	if err != nil {
		return &pb.RegisterSeriesSnapshotRsp{RetInfo: retinfo.Error(errorCode(err), err)}, nil
	}
	return &pb.RegisterSeriesSnapshotRsp{RetInfo: retinfo.Success("success"), SeriesHash: result.SeriesHash, ExpectedCount: result.ExpectedCount, AlreadyRegistered: result.AlreadyRegistered}, nil
}

func (s *Service) EnsureDatasetPeriod(ctx context.Context, req *pb.EnsureDatasetPeriodReq) (*pb.EnsureDatasetPeriodRsp, error) {
	if req == nil || req.GetExpectation() == nil {
		return &pb.EnsureDatasetPeriodRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("expectation is required"))}, nil
	}
	if err := validatePeriodNodeID(req.GetNodeId(), s.nodeID); err != nil {
		return &pb.EnsureDatasetPeriodRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	if err := s.validateAuth(req.GetAuthInfo()); err != nil {
		return &pb.EnsureDatasetPeriodRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	if req.GetExpectation().GetDeadlineAt() <= 0 {
		return &pb.EnsureDatasetPeriodRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("deadline_at must be positive"))}, nil
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
	if err := validatePeriodNodeID(req.GetNodeId(), s.nodeID); err != nil {
		return &pb.GetDatasetPeriodStatusRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	if err := s.validateAuth(req.GetAuthInfo()); err != nil {
		return &pb.GetDatasetPeriodStatusRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	exp := req.GetExpectation()
	if strings.TrimSpace(exp.GetSeriesHash()) == "" {
		// 只带周期键的新形态：周期不存在返回 found=false，include_progress 时附带缺失序号。
		progress, found, err := s.store.PeriodStatusByKey(ctx, exp.GetSpaceId(), exp.GetDatasetId(), exp.GetFrequency(), exp.GetPeriodTime(), req.GetIncludeProgress())
		if err != nil {
			return &pb.GetDatasetPeriodStatusRsp{RetInfo: retinfo.Error(errorCode(err), err)}, nil
		}
		if !found {
			return &pb.GetDatasetPeriodStatusRsp{RetInfo: retinfo.Success("success"), Found: false}, nil
		}
		rsp := &pb.GetDatasetPeriodStatusRsp{RetInfo: retinfo.Success("success"), Found: true, Status: progress.Status, SeriesHash: progress.SeriesHash, ExpectedCount: progress.ExpectedCount, DeadlineAt: progress.DeadlineAt}
		if req.GetIncludeProgress() {
			rsp.MissingSeriesIndexes = pebble.PeriodMissingIndexes(progress.ExpectedCount, progress.Bitmap)
			rsp.SuccessfulCount = progress.ExpectedCount - uint32(len(rsp.MissingSeriesIndexes))
		}
		return rsp, nil
	}
	progress, err := s.store.GetDatasetPeriodStatus(ctx, periodExpectation(exp))
	if err != nil {
		return &pb.GetDatasetPeriodStatusRsp{RetInfo: retinfo.Error(errorCode(err), err)}, nil
	}
	return &pb.GetDatasetPeriodStatusRsp{RetInfo: retinfo.Success("success"), Found: true, Status: progress.Status, SeriesHash: progress.SeriesHash, ExpectedCount: progress.ExpectedCount, DeadlineAt: progress.DeadlineAt}, nil
}

func (s *Service) CommitTimeSeriesBatch(ctx context.Context, req *pb.CommitTimeSeriesBatchReq) (*pb.CommitTimeSeriesBatchRsp, error) {
	if req == nil || req.GetExpectation() == nil || len(req.GetItems()) == 0 {
		return &pb.CommitTimeSeriesBatchRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("expectation and items are required"))}, nil
	}
	if err := validatePeriodNodeID(req.GetNodeId(), s.nodeID); err != nil {
		return &pb.CommitTimeSeriesBatchRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
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
	commitExp := periodExpectation(req.GetExpectation())
	commitExp.AllowedLatenessSeconds = req.GetAllowedLatenessSeconds()
	result, err := s.store.CommitTimeSeriesBatch(ctx, commitExp, items, req.GetSourceEventId(), req.GetWriteSource())
	if err != nil {
		return &pb.CommitTimeSeriesBatchRsp{RetInfo: retinfo.Error(errorCode(err), err)}, nil
	}
	return &pb.CommitTimeSeriesBatchRsp{RetInfo: retinfo.Success("success"), Keys: keys, PeriodStatus: result.Status, AcceptedSeriesIndexes: result.AcceptedSeriesIndexes}, nil
}

func (s *Service) RecordDatasetPeriodFailures(ctx context.Context, req *pb.RecordDatasetPeriodFailuresReq) (*pb.RecordDatasetPeriodFailuresRsp, error) {
	if req == nil || req.GetExpectation() == nil || len(req.GetSeriesIndexes()) == 0 {
		return &pb.RecordDatasetPeriodFailuresRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("expectation and series_indexes are required"))}, nil
	}
	if err := validatePeriodNodeID(req.GetNodeId(), s.nodeID); err != nil {
		return &pb.RecordDatasetPeriodFailuresRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
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

func validatePeriodNodeID(nodeID, expectedNodeID string) error {
	if strings.TrimSpace(nodeID) == "" {
		return errors.New("node_id is required")
	}
	if nodeID != expectedNodeID {
		return errors.New("node_id does not match DataNode")
	}
	return nil
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
		SeriesHash: in.GetSeriesHash(), ExpectedCount: in.GetExpectedCount(), DeadlineAt: in.GetDeadlineAt(), ReservationID: in.GetReservationId(), SeriesSnapshot: seriesSnapshot,
	}
}
