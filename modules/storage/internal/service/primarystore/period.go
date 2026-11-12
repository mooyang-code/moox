package primarystore

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-go/log"

	frequencypkg "github.com/mooyang-code/moox/packages/frequency"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/seriesset"
)

// RegisterSeriesSnapshot 登记数据集的完整标的列表与生效周期。
func (s *Service) RegisterSeriesSnapshot(ctx context.Context, req *pb.PrimaryRegisterSeriesSnapshotReq) (*pb.PrimaryRegisterSeriesSnapshotRsp, error) {
	if req == nil || len(req.GetSeries()) == 0 {
		return &pb.PrimaryRegisterSeriesSnapshotRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("series snapshot is required"))}, nil
	}
	if err := s.authorizeRequest(req.GetAuthInfo()); err != nil {
		return &pb.PrimaryRegisterSeriesSnapshotRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	if strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetDatasetId()) == "" {
		return &pb.PrimaryRegisterSeriesSnapshotRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and dataset_id are required"))}, nil
	}
	ctx = s.requestContext(ctx)
	scope := &pb.DatasetPeriodExpectation{SpaceId: req.GetSpaceId(), DatasetId: req.GetDatasetId(), Frequency: req.GetFrequency()}
	if err := validatePeriodDataset(ctx, scope); err != nil {
		return &pb.PrimaryRegisterSeriesSnapshotRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	if err := validateSnapshotAlignment(ctx, req); err != nil {
		return &pb.PrimaryRegisterSeriesSnapshotRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	if err := requireActiveDataset(ctx, req.GetSpaceId(), req.GetDatasetId()); err != nil {
		return &pb.PrimaryRegisterSeriesSnapshotRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	series := make([]seriesset.Series, len(req.GetSeries()))
	for i, entry := range req.GetSeries() {
		series[i] = seriesset.Series{SubjectID: entry.GetSubjectId(), SeriesTag: entry.GetSeriesTag()}
	}
	result, err := s.store.RegisterSeriesSnapshot(ctx, req.GetSpaceId(), req.GetDatasetId(), req.GetFrequency(), series, req.GetEffectiveFrom())
	if err != nil {
		return &pb.PrimaryRegisterSeriesSnapshotRsp{RetInfo: retinfo.Error(pebble.ErrorCode(err), err)}, nil
	}
	return &pb.PrimaryRegisterSeriesSnapshotRsp{RetInfo: retinfo.Success("success"), SeriesHash: result.SeriesHash, ExpectedCount: result.ExpectedCount, AlreadyRegistered: result.AlreadyRegistered}, nil
}

func (s *Service) CommitTimeSeriesBatch(ctx context.Context, req *pb.PrimaryCommitTimeSeriesBatchReq) (*pb.PrimaryCommitTimeSeriesBatchRsp, error) {
	if req == nil || req.GetExpectation() == nil || len(req.GetItems()) == 0 {
		return &pb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("expectation and items are required"))}, nil
	}
	if err := s.authorizeRequest(req.GetAuthInfo()); err != nil {
		return &pb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	exp := req.GetExpectation()
	if strings.TrimSpace(exp.GetSpaceId()) == "" || strings.TrimSpace(exp.GetDatasetId()) == "" {
		return &pb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and dataset_id are required"))}, nil
	}
	ctx = s.requestContext(ctx)
	if err := validatePeriodDataset(ctx, exp); err != nil {
		return &pb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	rows := make([]*pb.RowFieldUpsert, 0, len(req.GetItems()))
	for _, item := range req.GetItems() {
		if item == nil || item.GetRow() == nil {
			return &pb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("batch item row is required"))}, nil
		}
		rows = append(rows, item.GetRow())
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
	// 周期提交是 Collector 每个周期写入行情的主路径：数据集的最近运行、最近成功与输出水位都要在这里记录，
	// 否则 Monitor 只能看到旧的 UpsertFields（补采、导入）留下的指标，把数据误判成“入库落后”。
	if err := requireActiveDataset(ctx, exp.GetSpaceId(), exp.GetDatasetId()); err != nil {
		s.observeTimeSeriesRows(ctx, rows, "error", false)
		return &pb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	items := make([]pebble.TimeSeriesBatchItem, 0, len(rows))
	keys := make([]*pb.RowKey, 0, len(rows))
	for i, row := range rows {
		items = append(items, pebble.TimeSeriesBatchItem{SeriesIndex: req.GetItems()[i].GetSeriesIndex(), Row: row})
		keys = append(keys, row.GetKey())
	}
	commitExp := periodExpectation(exp)
	commitExp.AllowedLatenessSeconds = datasetAllowedLateness(ctx, exp)
	result, err := s.store.CommitTimeSeriesBatch(ctx, commitExp, items)
	if err != nil {
		s.observeTimeSeriesRows(ctx, rows, "error", false)
		var deadlinePassed pebble.PeriodDeadlinePassedError
		if s.rejection != nil && errors.As(err, &deadlinePassed) {
			s.rejection.ObserveDeadlineRejection(exp.GetSpaceId(), exp.GetDatasetId(), exp.GetFrequency(), time.Now())
		}
		return &pb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: retinfo.Error(pebble.ErrorCode(err), err)}, nil
	}
	s.observeTimeSeriesRows(ctx, rows, "success", true)
	return &pb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: retinfo.Success("success"), Keys: keys, PeriodStatus: result.Status, AcceptedSeriesIndexes: result.AcceptedSeriesIndexes}, nil
}

// periodExpectation 把请求里的周期期望转成存储层的结构。
func periodExpectation(in *pb.DatasetPeriodExpectation) pebble.DatasetPeriodExpectation {
	seriesSnapshot := make([]pebble.DatasetPeriodSeries, len(in.GetSeriesSnapshot()))
	for index, row := range in.GetSeriesSnapshot() {
		seriesSnapshot[index] = pebble.DatasetPeriodSeries{SeriesIndex: row.GetSeriesIndex(), SubjectID: row.GetSubjectId(), SeriesTag: row.GetSeriesTag()}
	}
	return pebble.DatasetPeriodExpectation{
		SpaceID: in.GetSpaceId(), DatasetID: in.GetDatasetId(), Frequency: in.GetFrequency(), PeriodTime: in.GetPeriodTime(),
		SeriesHash: in.GetSeriesHash(), ExpectedCount: in.GetExpectedCount(), DeadlineAt: in.GetDeadlineAt(), ReservationID: in.GetReservationId(), SeriesSnapshot: seriesSnapshot,
	}
}

// datasetAlignment 是数据集声明的分桶方式：session 表示按交易时段从开盘起切分，其余（含未声明）按 epoch UTC。
func datasetAlignment(ctx context.Context, spaceID, datasetID string) string {
	snapshot := metadata.RequestSnapshotFromContext(ctx)
	if snapshot == nil {
		return ""
	}
	dataset, ok := snapshot.GetDataset(spaceID, datasetID)
	if !ok || dataset == nil {
		return ""
	}
	return strings.TrimSpace(dataset.GetAttributes()["alignment"])
}

// validateSnapshotAlignment 检查快照生效点与频率边界对齐：epoch 数据集的日内周期按 epoch 对齐；
// session 数据集（A 股等）的桶从各交易时段开盘起切分，不在 epoch 边界上，存储层只要求整分钟。
func validateSnapshotAlignment(ctx context.Context, req *pb.PrimaryRegisterSeriesSnapshotReq) error {
	if datasetAlignment(ctx, req.GetSpaceId(), req.GetDatasetId()) == "session" {
		return nil
	}
	parsed, err := frequencypkg.Parse(req.GetFrequency())
	if err != nil {
		return nil // 频率本身的校验在存储层
	}
	step := int64(parsed.Duration() / time.Second)
	if step <= 0 || step >= 24*60*60 {
		return nil // 日、周、月的边界取决于市场时区，存储层只要求整分钟
	}
	if at := req.GetEffectiveFrom(); at > 0 && at%step != 0 {
		return errors.New("effective_from must be aligned to the frequency boundary")
	}
	return nil
}

// validatePeriodDataset 检查周期提交与快照登记的目标：数据集存在于请求快照、是时序数据且频率与声明一致。
func validatePeriodDataset(ctx context.Context, expectation *pb.DatasetPeriodExpectation) error {
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

// datasetAllowedLateness 读取数据集属性 allowed_lateness_seconds；缺失或非法时返回 0，由存储层采用默认值。
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
		log.WarnContextf(ctx, "dataset %s/%s has no valid allowed_lateness_seconds; the store falls back to its default", exp.GetSpaceId(), exp.GetDatasetId())
		return 0
	}
	return seconds
}
