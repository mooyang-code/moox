package primarystore

import (
	"context"
	"errors"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type periodDataNodeClient interface {
	EnsureDatasetPeriod(context.Context, *pb.EnsureDatasetPeriodReq) (*pb.EnsureDatasetPeriodRsp, error)
	CommitTimeSeriesBatch(context.Context, *pb.CommitTimeSeriesBatchReq) (*pb.CommitTimeSeriesBatchRsp, error)
	RecordDatasetPeriodFailures(context.Context, *pb.RecordDatasetPeriodFailuresReq) (*pb.RecordDatasetPeriodFailuresRsp, error)
}

func (s *Service) EnsureDatasetPeriod(ctx context.Context, req *pb.PrimaryEnsureDatasetPeriodReq) (*pb.PrimaryEnsureDatasetPeriodRsp, error) {
	if req == nil || req.GetExpectation() == nil {
		return &pb.PrimaryEnsureDatasetPeriodRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("expectation is required"))}, nil
	}
	if err := s.authorizeRequest(req.GetAuthInfo()); err != nil {
		return &pb.PrimaryEnsureDatasetPeriodRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	exp := req.GetExpectation()
	if strings.TrimSpace(exp.GetSpaceId()) == "" || strings.TrimSpace(exp.GetDatasetId()) == "" {
		return &pb.PrimaryEnsureDatasetPeriodRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and dataset_id are required"))}, nil
	}
	ctx = s.requestContext(ctx)
	node, err := s.resolve(ctx, exp.GetSpaceId(), exp.GetDatasetId())
	if err != nil {
		return &pb.PrimaryEnsureDatasetPeriodRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	periodNode, ok := node.(periodDataNodeClient)
	if !ok {
		return &pb.PrimaryEnsureDatasetPeriodRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errors.New("DataNode period RPC is unavailable"))}, nil
	}
	auth, err := s.signAuth(req.GetAuthInfo())
	if err != nil {
		return &pb.PrimaryEnsureDatasetPeriodRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	rsp, err := periodNode.EnsureDatasetPeriod(ctx, &pb.EnsureDatasetPeriodReq{AuthInfo: auth, Expectation: exp})
	if err != nil {
		return &pb.PrimaryEnsureDatasetPeriodRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	return &pb.PrimaryEnsureDatasetPeriodRsp{RetInfo: rsp.GetRetInfo(), Status: rsp.GetStatus()}, nil
}

func (s *Service) RecordDatasetPeriodFailures(ctx context.Context, req *pb.PrimaryRecordDatasetPeriodFailuresReq) (*pb.PrimaryRecordDatasetPeriodFailuresRsp, error) {
	if req == nil || req.GetExpectation() == nil || len(req.GetSeriesIndexes()) == 0 {
		return &pb.PrimaryRecordDatasetPeriodFailuresRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("expectation and series_indexes are required"))}, nil
	}
	if err := rejectMooxSkillWrite(req.GetAuthInfo()); err != nil {
		return &pb.PrimaryRecordDatasetPeriodFailuresRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	if err := s.authorizeRequest(req.GetAuthInfo()); err != nil {
		return &pb.PrimaryRecordDatasetPeriodFailuresRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	if strings.EqualFold(strings.TrimSpace(req.GetAuthInfo().GetAppId()), "scf-market-canary") {
		return &pb.PrimaryRecordDatasetPeriodFailuresRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, errors.New("read-only primary credential"))}, nil
	}
	exp := req.GetExpectation()
	if strings.TrimSpace(exp.GetSpaceId()) == "" || strings.TrimSpace(exp.GetDatasetId()) == "" {
		return &pb.PrimaryRecordDatasetPeriodFailuresRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and dataset_id are required"))}, nil
	}
	ctx = s.requestContext(ctx)
	ownerKey := &pb.RowKey{SpaceId: exp.GetSpaceId(), DatasetId: exp.GetDatasetId()}
	if err := validateDatasetWriteOwner(ctx, req.GetAuthInfo(), []*pb.RowFieldUpsert{{Key: ownerKey}}); err != nil {
		return &pb.PrimaryRecordDatasetPeriodFailuresRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	node, err := s.resolve(ctx, exp.GetSpaceId(), exp.GetDatasetId())
	if err != nil {
		return &pb.PrimaryRecordDatasetPeriodFailuresRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	periodNode, ok := node.(periodDataNodeClient)
	if !ok {
		return &pb.PrimaryRecordDatasetPeriodFailuresRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errors.New("DataNode period RPC is unavailable"))}, nil
	}
	auth, err := s.signAuth(req.GetAuthInfo())
	if err != nil {
		return &pb.PrimaryRecordDatasetPeriodFailuresRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	rsp, err := periodNode.RecordDatasetPeriodFailures(ctx, &pb.RecordDatasetPeriodFailuresReq{AuthInfo: auth, Expectation: exp, SeriesIndexes: req.GetSeriesIndexes()})
	if err != nil {
		return &pb.PrimaryRecordDatasetPeriodFailuresRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	return &pb.PrimaryRecordDatasetPeriodFailuresRsp{RetInfo: rsp.GetRetInfo(), PeriodStatus: rsp.GetPeriodStatus()}, nil
}

func (s *Service) CommitTimeSeriesBatch(ctx context.Context, req *pb.PrimaryCommitTimeSeriesBatchReq) (*pb.PrimaryCommitTimeSeriesBatchRsp, error) {
	if req == nil || req.GetExpectation() == nil || len(req.GetItems()) == 0 {
		return &pb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("expectation and items are required"))}, nil
	}
	if err := rejectMooxSkillWrite(req.GetAuthInfo()); err != nil {
		return &pb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	if err := s.authorizeRequest(req.GetAuthInfo()); err != nil {
		return &pb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	if req.GetAuthInfo().GetAppId() == "scf-market-canary" {
		return &pb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, errors.New("read-only primary credential"))}, nil
	}
	exp := req.GetExpectation()
	rows := make([]*pb.RowFieldUpsert, 0, len(req.GetItems()))
	for _, item := range req.GetItems() {
		if item == nil || item.GetRow() == nil {
			return &pb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("batch item row is required"))}, nil
		}
		rows = append(rows, item.GetRow())
	}
	if err := rejectUnauthorizedUpsertSemantics(rows, req.GetWriteSource()); err != nil {
		return &pb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	rows = normalizeStockCNSeriesTags(rows)
	ctx = s.requestContext(ctx)
	if err := validateDatasetWriteOwner(ctx, req.GetAuthInfo(), rows); err != nil {
		return &pb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	if batchValidator, ok := s.validate.(interface {
		ValidateRows(context.Context, []*pb.RowFieldUpsert) error
	}); ok {
		if err := batchValidator.ValidateRows(ctx, rows); err != nil {
			return &pb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
		}
	}
	for _, row := range rows {
		if row.GetKey() == nil || row.GetKey().GetSpaceId() != exp.GetSpaceId() || row.GetKey().GetDatasetId() != exp.GetDatasetId() {
			return &pb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("row dataset identity does not match period expectation"))}, nil
		}
	}
	node, err := s.resolve(ctx, exp.GetSpaceId(), exp.GetDatasetId())
	if err != nil {
		return &pb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	periodNode, ok := node.(periodDataNodeClient)
	if !ok {
		return &pb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errors.New("DataNode period RPC is unavailable"))}, nil
	}
	auth, err := s.signAuth(req.GetAuthInfo())
	if err != nil {
		return &pb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	items := make([]*pb.TimeSeriesBatchRow, 0, len(rows))
	for i, row := range rows {
		items = append(items, &pb.TimeSeriesBatchRow{SeriesIndex: req.GetItems()[i].GetSeriesIndex(), Row: row})
	}
	rsp, err := periodNode.CommitTimeSeriesBatch(ctx, &pb.CommitTimeSeriesBatchReq{
		AuthInfo: auth, Expectation: exp, Items: items, SourceEventId: req.GetSourceEventId(), WriteSource: req.GetWriteSource(),
	})
	if err != nil {
		return &pb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	return &pb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: rsp.GetRetInfo(), Keys: rsp.GetKeys(), PeriodStatus: rsp.GetPeriodStatus()}, nil
}
