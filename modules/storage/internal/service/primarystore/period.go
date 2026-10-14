package primarystore

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"trpc.group/trpc-go/trpc-go/log"

	frequencypkg "github.com/mooyang-code/moox/packages/frequency"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type snapshotDataNodeClient interface {
	RegisterSeriesSnapshot(context.Context, *pb.RegisterSeriesSnapshotReq) (*pb.RegisterSeriesSnapshotRsp, error)
}

type periodDataNodeClient interface {
	CommitTimeSeriesBatch(context.Context, *pb.CommitTimeSeriesBatchReq) (*pb.CommitTimeSeriesBatchRsp, error)
}

// RegisterSeriesSnapshot 把数据集的完整标的列表与生效周期转发给所属 DataNode。
func (s *Service) RegisterSeriesSnapshot(ctx context.Context, req *pb.PrimaryRegisterSeriesSnapshotReq) (*pb.PrimaryRegisterSeriesSnapshotRsp, error) {
	if req == nil || len(req.GetSeries()) == 0 {
		return &pb.PrimaryRegisterSeriesSnapshotRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("series snapshot is required"))}, nil
	}
	if err := s.authorizeRequest(req.GetAuthInfo()); err != nil {
		return &pb.PrimaryRegisterSeriesSnapshotRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	if err := rejectCollectorPeriodWriteCredential(req.GetAuthInfo()); err != nil {
		return &pb.PrimaryRegisterSeriesSnapshotRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	if strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetDatasetId()) == "" {
		return &pb.PrimaryRegisterSeriesSnapshotRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and dataset_id are required"))}, nil
	}
	ctx = s.requestContext(ctx)
	scope := &pb.DatasetPeriodExpectation{SpaceId: req.GetSpaceId(), DatasetId: req.GetDatasetId(), Frequency: req.GetFrequency()}
	if err := validateCollectorPeriodDataset(ctx, req.GetAuthInfo(), scope); err != nil {
		return &pb.PrimaryRegisterSeriesSnapshotRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	node, err := s.resolve(ctx, req.GetSpaceId(), req.GetDatasetId())
	if err != nil {
		return &pb.PrimaryRegisterSeriesSnapshotRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	periodNode, ok := node.(snapshotDataNodeClient)
	if !ok {
		return &pb.PrimaryRegisterSeriesSnapshotRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errors.New("DataNode period RPC is unavailable"))}, nil
	}
	auth, err := s.signAuth(req.GetAuthInfo())
	if err != nil {
		return &pb.PrimaryRegisterSeriesSnapshotRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	rsp, err := periodNode.RegisterSeriesSnapshot(ctx, &pb.RegisterSeriesSnapshotReq{
		AuthInfo: auth, SpaceId: req.GetSpaceId(), DatasetId: req.GetDatasetId(), Frequency: req.GetFrequency(),
		Series: req.GetSeries(), EffectiveFrom: req.GetEffectiveFrom(),
	})
	if err != nil {
		return &pb.PrimaryRegisterSeriesSnapshotRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	if rsp == nil || rsp.GetRetInfo() == nil {
		return &pb.PrimaryRegisterSeriesSnapshotRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errors.New("DataNode returned no snapshot response"))}, nil
	}
	return &pb.PrimaryRegisterSeriesSnapshotRsp{RetInfo: rsp.GetRetInfo(), SeriesHash: rsp.GetSeriesHash(), ExpectedCount: rsp.GetExpectedCount(), AlreadyRegistered: rsp.GetAlreadyRegistered()}, nil
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
	// 周期提交是 Collector 每个周期写入行情的主路径：数据集的最近运行、最近成功与输出水位都要在这里记录，
	// 否则 Monitor 只能看到旧的 UpsertFields（补采、导入）留下的指标，把数据误判成“入库落后”。
	node, err := s.resolve(ctx, exp.GetSpaceId(), exp.GetDatasetId())
	if err != nil {
		s.observeTimeSeriesRows(ctx, rows, "error", false, false)
		return &pb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	periodNode, ok := node.(periodDataNodeClient)
	if !ok {
		s.observeTimeSeriesRows(ctx, rows, "error", false, false)
		return &pb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errors.New("DataNode period RPC is unavailable"))}, nil
	}
	auth, err := s.signAuth(req.GetAuthInfo())
	if err != nil {
		s.observeTimeSeriesRows(ctx, rows, "error", false, false)
		return &pb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	items := make([]*pb.TimeSeriesBatchRow, 0, len(rows))
	for i, row := range rows {
		items = append(items, &pb.TimeSeriesBatchRow{SeriesIndex: req.GetItems()[i].GetSeriesIndex(), Row: row})
	}
	rsp, err := periodNode.CommitTimeSeriesBatch(ctx, &pb.CommitTimeSeriesBatchReq{
		AuthInfo: auth, Expectation: exp, Items: items, SourceEventId: req.GetSourceEventId(), WriteSource: req.GetWriteSource(),
		AllowedLatenessSeconds: datasetAllowedLateness(ctx, exp),
	})
	if err != nil {
		s.observeTimeSeriesRows(ctx, rows, "error", false, false)
		return &pb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	if rsp == nil || rsp.GetRetInfo() == nil {
		s.observeTimeSeriesRows(ctx, rows, "error", false, false)
		return &pb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errors.New("DataNode returned no period response"))}, nil
	}
	s.observePeriodCommit(ctx, rows, rsp)
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
	if !frequencypkg.IsCanonical(frequency) {
		return errors.New("period frequency is invalid")
	}
	if dataset.GetFreq() != frequency {
		return errors.New("period frequency does not match the Dataset declaration")
	}
	return nil
}

// datasetAllowedLateness 读取数据集属性 allowed_lateness_seconds；缺失或非法时返回 0，由 DataNode 采用默认值。
func datasetAllowedLateness(ctx context.Context, exp *pb.DatasetPeriodExpectation) int64 {
	snapshot := metadata.RequestSnapshotFromContext(ctx)
	if snapshot == nil {
		return 0
	}
	dataset, ok := snapshot.GetDataset(exp.GetSpaceId(), exp.GetDatasetId())
	if !ok || dataset == nil {
		return 0
	}
	seconds, err := strconv.ParseInt(strings.TrimSpace(dataset.GetAttributes()["allowed_lateness_seconds"]), 10, 64)
	if err != nil || seconds <= 0 {
		log.WarnContextf(ctx, "dataset %s/%s has no valid allowed_lateness_seconds; DataNode falls back to its default", exp.GetSpaceId(), exp.GetDatasetId())
		return 0
	}
	return seconds
}

// observePeriodCommit 按 DataNode 的提交结果记录数据集观测：提交了行记为 success 并推进输出水位；
// 没有行被接受（全部重复或被拒）记为 empty，只更新最近运行；DataNode 返回失败记为 error。
func (s *Service) observePeriodCommit(ctx context.Context, rows []*pb.RowFieldUpsert, rsp *pb.CommitTimeSeriesBatchRsp) {
	switch {
	case rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS:
		s.observeTimeSeriesRows(ctx, rows, "error", false, false)
	case len(rsp.GetKeys()) > 0:
		s.observeTimeSeriesRows(ctx, rows, "success", true, len(rsp.GetKeys()) == len(rows))
	default:
		s.observeTimeSeriesRows(ctx, rows, "empty", false, false)
	}
}
