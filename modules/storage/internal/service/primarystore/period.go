package primarystore

import (
	"context"
	"errors"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/report"
)

type periodDataNodeClient interface {
	EnsureDatasetPeriod(context.Context, *pb.EnsureDatasetPeriodReq) (*pb.EnsureDatasetPeriodRsp, error)
	GetDatasetPeriodStatus(context.Context, *pb.GetDatasetPeriodStatusReq) (*pb.GetDatasetPeriodStatusRsp, error)
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
	if err := rejectCollectorPeriodWriteCredential(req.GetAuthInfo()); err != nil {
		return &pb.PrimaryEnsureDatasetPeriodRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	exp := req.GetExpectation()
	if strings.TrimSpace(exp.GetSpaceId()) == "" || strings.TrimSpace(exp.GetDatasetId()) == "" {
		return &pb.PrimaryEnsureDatasetPeriodRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and dataset_id are required"))}, nil
	}
	ctx = s.requestContext(ctx)
	if err := validateCollectorPeriodDataset(ctx, req.GetAuthInfo(), exp); err != nil {
		return &pb.PrimaryEnsureDatasetPeriodRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	if exp.GetDeadlineAt() <= 0 {
		return &pb.PrimaryEnsureDatasetPeriodRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("deadline_at must be positive"))}, nil
	}
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
	if rsp == nil || rsp.GetRetInfo() == nil {
		return &pb.PrimaryEnsureDatasetPeriodRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errors.New("DataNode returned no period response"))}, nil
	}
	return &pb.PrimaryEnsureDatasetPeriodRsp{RetInfo: rsp.GetRetInfo(), Status: rsp.GetStatus(), DeadlineAt: rsp.GetDeadlineAt()}, nil
}

func (s *Service) GetDatasetPeriodStatus(ctx context.Context, req *pb.PrimaryGetDatasetPeriodStatusReq) (*pb.PrimaryGetDatasetPeriodStatusRsp, error) {
	if req == nil || req.GetExpectation() == nil {
		return &pb.PrimaryGetDatasetPeriodStatusRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("expectation is required"))}, nil
	}
	if err := s.authorizeRequest(req.GetAuthInfo()); err != nil {
		return &pb.PrimaryGetDatasetPeriodStatusRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	exp := req.GetExpectation()
	if strings.TrimSpace(exp.GetSpaceId()) == "" || strings.TrimSpace(exp.GetDatasetId()) == "" {
		return &pb.PrimaryGetDatasetPeriodStatusRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and dataset_id are required"))}, nil
	}
	ctx = s.requestContext(ctx)
	if err := validateCollectorPeriodDataset(ctx, req.GetAuthInfo(), exp); err != nil {
		return &pb.PrimaryGetDatasetPeriodStatusRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	node, err := s.resolve(ctx, exp.GetSpaceId(), exp.GetDatasetId())
	if err != nil {
		return &pb.PrimaryGetDatasetPeriodStatusRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	periodNode, ok := node.(periodDataNodeClient)
	if !ok {
		return &pb.PrimaryGetDatasetPeriodStatusRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errors.New("DataNode period RPC is unavailable"))}, nil
	}
	auth, err := s.signAuth(req.GetAuthInfo())
	if err != nil {
		return &pb.PrimaryGetDatasetPeriodStatusRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	rsp, err := periodNode.GetDatasetPeriodStatus(ctx, &pb.GetDatasetPeriodStatusReq{AuthInfo: auth, Expectation: exp})
	if err != nil {
		return &pb.PrimaryGetDatasetPeriodStatusRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	if rsp == nil || rsp.GetRetInfo() == nil {
		return &pb.PrimaryGetDatasetPeriodStatusRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errors.New("DataNode returned no period response"))}, nil
	}
	return &pb.PrimaryGetDatasetPeriodStatusRsp{RetInfo: rsp.GetRetInfo(), Status: rsp.GetStatus(), SeriesHash: rsp.GetSeriesHash(), ExpectedCount: rsp.GetExpectedCount(), DeadlineAt: rsp.GetDeadlineAt()}, nil
}

func (s *Service) RecordDatasetPeriodFailures(ctx context.Context, req *pb.PrimaryRecordDatasetPeriodFailuresReq) (*pb.PrimaryRecordDatasetPeriodFailuresRsp, error) {
	if req == nil || req.GetExpectation() == nil || len(req.GetSeriesIndexes()) == 0 {
		return &pb.PrimaryRecordDatasetPeriodFailuresRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("expectation and series_indexes are required"))}, nil
	}
	if err := s.authorizeRequest(req.GetAuthInfo()); err != nil {
		return &pb.PrimaryRecordDatasetPeriodFailuresRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	if err := rejectCollectorPeriodWriteCredential(req.GetAuthInfo()); err != nil {
		return &pb.PrimaryRecordDatasetPeriodFailuresRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	exp := req.GetExpectation()
	if strings.TrimSpace(exp.GetSpaceId()) == "" || strings.TrimSpace(exp.GetDatasetId()) == "" {
		return &pb.PrimaryRecordDatasetPeriodFailuresRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and dataset_id are required"))}, nil
	}
	ctx = s.requestContext(ctx)
	if err := validateCollectorPeriodDataset(ctx, req.GetAuthInfo(), exp); err != nil {
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
	if rsp == nil || rsp.GetRetInfo() == nil {
		return &pb.PrimaryRecordDatasetPeriodFailuresRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errors.New("DataNode returned no period response"))}, nil
	}
	return &pb.PrimaryRecordDatasetPeriodFailuresRsp{RetInfo: rsp.GetRetInfo(), PeriodStatus: rsp.GetPeriodStatus(), Results: rsp.GetResults()}, nil
}

func (s *Service) CommitTimeSeriesBatch(ctx context.Context, req *pb.PrimaryCommitTimeSeriesBatchReq) (*pb.PrimaryCommitTimeSeriesBatchRsp, error) {
	if req == nil || req.GetExpectation() == nil || len(req.GetItems()) == 0 {
		return &pb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("expectation and items are required"))}, nil
	}
	if err := s.authorizeRequest(req.GetAuthInfo()); err != nil {
		return &pb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	if err := rejectCollectorPeriodWriteCredential(req.GetAuthInfo()); err != nil {
		return &pb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	exp := req.GetExpectation()
	if strings.TrimSpace(exp.GetSpaceId()) == "" || strings.TrimSpace(exp.GetDatasetId()) == "" {
		return &pb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and dataset_id are required"))}, nil
	}
	ctx = s.requestContext(ctx)
	if err := validateCollectorPeriodDataset(ctx, req.GetAuthInfo(), exp); err != nil {
		return &pb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
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
	if rsp == nil || rsp.GetRetInfo() == nil {
		return &pb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errors.New("DataNode returned no period response"))}, nil
	}
	return &pb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: rsp.GetRetInfo(), Keys: rsp.GetKeys(), PeriodStatus: rsp.GetPeriodStatus(), AcceptedSeriesIndexes: rsp.GetAcceptedSeriesIndexes()}, nil
}

func rejectCollectorPeriodWriteCredential(auth *pb.AuthInfo) error {
	if strings.EqualFold(strings.TrimSpace(auth.GetAppId()), "scf-market-canary") {
		return errors.New("read-only primary credential")
	}
	return nil
}

func validateCollectorPeriodDataset(ctx context.Context, auth *pb.AuthInfo, expectation *pb.DatasetPeriodExpectation) error {
	if auth == nil || !isOwnedAppID(auth.GetAppId(), "collector") {
		return errors.New("period RPC requires Collector caller identity")
	}
	if expectation == nil || strings.TrimSpace(expectation.GetSpaceId()) == "" || strings.TrimSpace(expectation.GetDatasetId()) == "" {
		return errors.New("period expectation requires space_id and dataset_id")
	}
	snapshot := metadata.RequestSnapshotFromContext(ctx)
	if snapshot == nil {
		return errors.New("metadata cache snapshot is unavailable")
	}
	dataset, ok := snapshot.GetDataset(expectation.GetSpaceId(), expectation.GetDatasetId())
	if !ok || dataset == nil || dataset.GetSpaceId() != expectation.GetSpaceId() || dataset.GetDatasetId() != expectation.GetDatasetId() {
		return errors.New("period Dataset is not present in the request metadata snapshot")
	}
	attrs := dataset.GetAttributes()
	if attrs["owner_module"] != "collector" || attrs["dataset_role"] != "raw_collection" {
		return errors.New("period Dataset must be Collector-owned raw_collection data")
	}
	if dataset.GetDataKind() != pb.DataKind_DATA_KIND_TIME_SERIES {
		return errors.New("period Dataset must be time_series")
	}
	frequency := expectation.GetFrequency()
	if _, err := report.ParseDatasetFrequency(frequency); err != nil {
		return errors.New("period frequency is invalid")
	}
	for _, declared := range dataset.GetFreqs() {
		if declared == frequency {
			return nil
		}
	}
	return errors.New("period frequency does not match the Dataset declaration")
}
