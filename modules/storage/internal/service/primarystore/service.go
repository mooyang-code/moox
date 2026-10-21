package primarystore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/report"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/log"
)

type Validator interface {
	ValidateRow(context.Context, *pb.RowFieldUpsert) error
}
type DataNodeClient interface {
	UpsertFields(context.Context, *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error)
	ReadRows(context.Context, *pb.ReadRowsReq) (*pb.ReadRowsRsp, error)
}

// rangeDataNodeClient 是 DataNode 的区间读取与计数入口；PrimaryStore 对所有调用方都转给它。
type rangeDataNodeClient interface {
	ReadTimeSeriesRows(context.Context, *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error)
	CountTimeSeriesRows(context.Context, *pb.CountTimeSeriesRowsReq) (*pb.CountTimeSeriesRowsRsp, error)
	ReadDatasetChanges(context.Context, *pb.ReadDatasetChangesReq) (*pb.ReadDatasetChangesRsp, error)
}

// Resolver 按空间与数据集找到负责的 DataNode。区间读取等能力通过类型断言发现。
type Resolver func(ctx context.Context, spaceID, datasetID string) (DataNodeClient, error)
type AuthSigner func(*pb.AuthInfo) (*pb.AuthInfo, error)
type Authorizer func(*pb.AuthInfo) error
type SnapshotProvider func() metadata.RequestSnapshot
type ResultDatasetResolver func(context.Context, string, string) (string, error)
type ViewSyncPointReader interface {
	MissingViewSyncPointDatasets(context.Context, string, string, string, []string) ([]string, error)
}
type routeKey struct{ spaceID, datasetID string }
type DatasetRunObserver interface {
	ObserveRun(report.DatasetObservation) error
}

// DeadlineRejectionObserver 记录“周期建立前截止已过”的拒绝：写入方取数晚于数据集截止（常见于采集方式切换时截止没跟上），
// 整期提交都会被拒，Monitor 据此告警。
type DeadlineRejectionObserver interface {
	ObserveDeadlineRejection(spaceID, datasetID, frequency string, at time.Time)
}

type Service struct {
	resolve   Resolver
	validate  Validator
	sign      AuthSigner
	authorize Authorizer
	snapshot  SnapshotProvider
	metrics   DatasetRunObserver
	rejection DeadlineRejectionObserver
	result    ResultDatasetResolver
	syncPoint ViewSyncPointReader
}

type Options struct {
	Node DataNodeClient
	// Resolver 为空时使用固定的 Node。
	Resolver       Resolver
	Validator      Validator
	AuthSigner     AuthSigner
	Authorizer     Authorizer
	Snapshot       SnapshotProvider
	DatasetMetrics DatasetRunObserver
	// DeadlineRejections 为空时不记录截止拒绝。
	DeadlineRejections DeadlineRejectionObserver
	ResultDataset      ResultDatasetResolver
	SyncPoints         ViewSyncPointReader
}

func New(opts Options) (*Service, error) {
	resolve := opts.Resolver
	if resolve == nil && opts.Node != nil {
		resolve = func(context.Context, string, string) (DataNodeClient, error) { return opts.Node, nil }
	}
	if resolve == nil {
		return nil, errors.New("data node resolver is required")
	}
	return &Service{
		resolve: resolve, validate: opts.Validator, sign: opts.AuthSigner, authorize: opts.Authorizer,
		snapshot: opts.Snapshot, metrics: opts.DatasetMetrics, rejection: opts.DeadlineRejections, result: opts.ResultDataset, syncPoint: opts.SyncPoints,
	}, nil
}

// requestContext captures one immutable metadata generation before a request
// starts grouping or routing rows. The snapshot is cache-only; it must never
// fall back to the persistent metadata store on the PrimaryStore hot path.
func (s *Service) requestContext(ctx context.Context) context.Context {
	if metadata.RequestSnapshotFromContext(ctx) != nil {
		return ctx
	}
	var snapshot metadata.RequestSnapshot
	if s.snapshot != nil {
		snapshot = s.snapshot()
	} else if provider, ok := s.validate.(interface {
		RequestSnapshot() metadata.RequestSnapshot
	}); ok {
		snapshot = provider.RequestSnapshot()
	}
	if snapshot == nil {
		return ctx
	}
	return metadata.WithRequestSnapshot(ctx, snapshot)
}

func (s *Service) UpsertFields(ctx context.Context, req *pb.PrimaryUpsertFieldsReq) (*pb.PrimaryUpsertFieldsRsp, error) {
	if req == nil || len(req.GetRows()) == 0 {
		return &pb.PrimaryUpsertFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("rows are required"))}, nil
	}
	if err := s.authorizeRequest(req.GetAuthInfo()); err != nil {
		return &pb.PrimaryUpsertFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	if req.GetAuthInfo().GetAppId() == "scf-market-canary" {
		return &pb.PrimaryUpsertFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, errors.New("read-only primary credential"))}, nil
	}
	if err := rejectUnauthorizedUpsertSemantics(req.GetRows(), req.GetWriteSource()); err != nil {
		return &pb.PrimaryUpsertFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	rows := normalizeStockCNSeriesTags(req.GetRows())
	ctx = s.requestContext(ctx)
	if err := validateDatasetWriteOwner(ctx, req.GetAuthInfo(), rows); err != nil {
		return &pb.PrimaryUpsertFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	groups := make(map[routeKey][]*pb.RowFieldUpsert)
	order := make([]routeKey, 0)
	if batchValidator, ok := s.validate.(interface {
		ValidateRows(context.Context, []*pb.RowFieldUpsert) error
	}); ok {
		if err := batchValidator.ValidateRows(ctx, rows); err != nil {
			return &pb.PrimaryUpsertFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
		}
	}
	for _, row := range rows {
		validator := s.validate
		if _, batched := s.validate.(interface {
			ValidateRows(context.Context, []*pb.RowFieldUpsert) error
		}); batched {
			validator = nil
		}
		if err := validateRow(ctx, row, validator); err != nil {
			return &pb.PrimaryUpsertFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
		}
		group := routeKey{spaceID: row.GetKey().GetSpaceId(), datasetID: row.GetKey().GetDatasetId()}
		if _, ok := groups[group]; !ok {
			order = append(order, group)
		}
		groups[group] = append(groups[group], row)
	}
	keys := make([]*pb.RowKey, 0, len(req.GetRows()))
	for _, group := range order {
		rows := groups[group]
		node, err := s.resolve(ctx, group.spaceID, group.datasetID)
		if err != nil {
			s.observeTimeSeriesRows(ctx, rows, "error", false, false)
			return &pb.PrimaryUpsertFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, fmt.Errorf("partial success after %d rows: route %s/%s: %w", len(keys), group.spaceID, group.datasetID, err)), Keys: keys}, nil
		}
		auth, err := s.signAuth(req.GetAuthInfo())
		if err != nil {
			s.observeTimeSeriesRows(ctx, rows, "error", false, false)
			return &pb.PrimaryUpsertFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, fmt.Errorf("partial success after %d rows: %w", len(keys), err)), Keys: keys}, nil
		}
		rsp, err := node.UpsertFields(ctx, &pb.UpsertFieldsReq{AuthInfo: auth, Rows: rows, SourceEventId: req.GetSourceEventId(), WriteSource: req.GetWriteSource()})
		if err != nil {
			s.observeTimeSeriesRows(ctx, rows, "error", false, false)
			return &pb.PrimaryUpsertFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, fmt.Errorf("partial success after %d rows: write %s/%s: %w", len(keys), group.spaceID, group.datasetID, err)), Keys: keys}, nil
		}
		if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			s.observeTimeSeriesRows(ctx, rows, "error", false, false)
			return &pb.PrimaryUpsertFieldsRsp{RetInfo: retinfo.Error(rsp.GetRetInfo().GetCode(), fmt.Errorf("partial success after %d rows: %s", len(keys), rsp.GetRetInfo().GetMsg())), Keys: keys}, nil
		}
		keys = append(keys, rsp.GetKeys()...)
		s.observeTimeSeriesRows(ctx, rows, "success", true, len(rsp.GetKeys()) == len(rows))
	}
	return &pb.PrimaryUpsertFieldsRsp{RetInfo: retinfo.Success("success"), Keys: keys}, nil
}

const stockCNDefaultSeriesTag = "default"

func normalizeStockCNSeriesTags(input []*pb.RowFieldUpsert) []*pb.RowFieldUpsert {
	rows := make([]*pb.RowFieldUpsert, 0, len(input))
	for _, row := range input {
		if row == nil {
			rows = append(rows, nil)
			continue
		}
		copyRow := proto.Clone(row).(*pb.RowFieldUpsert)
		key := copyRow.GetKey()
		series := key.GetTimeSeries()
		if key.GetSpaceId() == "stockcn" && key.GetDatasetId() == "dataset_stockcn_equity_kline_1m" && series != nil && strings.TrimSpace(series.GetSeriesTag()) == "" {
			series.SeriesTag = stockCNDefaultSeriesTag
		}
		rows = append(rows, copyRow)
	}
	return rows
}

// collectorWritableDataset 判断数据集是否归 Collector 所有：原始采集或重采样结果。
func collectorWritableDataset(attrs map[string]string) bool {
	if strings.TrimSpace(attrs["owner_module"]) != "collector" {
		return false
	}
	switch strings.TrimSpace(attrs["dataset_role"]) {
	case "raw_collection", "kline_resample_result":
		return true
	default:
		return false
	}
}

func isFactorAppID(appID string) bool {
	switch strings.ToLower(strings.TrimSpace(appID)) {
	case "factor", "moox-factor":
		return true
	default:
		return false
	}
}

func isOwnedAppID(appID, owner string) bool {
	appID = strings.ToLower(strings.TrimSpace(appID))
	owner = strings.ToLower(strings.TrimSpace(owner))
	if appID == owner || appID == "moox-"+owner {
		return true
	}
	return owner == "factor" && isFactorAppID(appID)
}

func validateDatasetWriteOwner(ctx context.Context, auth *pb.AuthInfo, rows []*pb.RowFieldUpsert) error {
	snapshot := metadata.RequestSnapshotFromContext(ctx)
	if snapshot == nil {
		return nil
	}
	checked := make(map[routeKey]struct{})
	keyHasTimeSeriesRows := make(map[routeKey]bool)
	for _, row := range rows {
		if row != nil && row.GetKey() != nil && row.GetKey().GetTimeSeries() != nil {
			keyHasTimeSeriesRows[routeKey{spaceID: row.GetKey().GetSpaceId(), datasetID: row.GetKey().GetDatasetId()}] = true
		}
	}
	for _, row := range rows {
		if row == nil || row.GetKey() == nil {
			continue
		}
		key := routeKey{spaceID: row.GetKey().GetSpaceId(), datasetID: row.GetKey().GetDatasetId()}
		if _, ok := checked[key]; ok {
			continue
		}
		checked[key] = struct{}{}
		dataset, ok := snapshot.GetDataset(key.spaceID, key.datasetID)
		if !ok || dataset == nil {
			continue
		}
		attrs := dataset.GetAttributes()
		if attrs["dataset_role"] == "factor_result" {
			return fmt.Errorf("dataset %s/%s requires WriteFactorRows", key.spaceID, key.datasetID)
		}
		// Collector 身份（含函数凭据）写时序行时，只能写自己拥有的采集与重采样数据集；
		// 记录型数据（任务结果等）不受此限制。
		if isOwnedAppID(auth.GetAppId(), "collector") && keyHasTimeSeriesRows[key] && !collectorWritableDataset(attrs) {
			return fmt.Errorf("dataset %s/%s is not Collector-owned collection data", key.spaceID, key.datasetID)
		}
		if attrs["write_owner"] != "factor" {
			continue
		}
		appID := strings.ToLower(strings.TrimSpace(auth.GetAppId()))
		if !isFactorAppID(appID) {
			return fmt.Errorf("dataset %s/%s is writable only by Factor", key.spaceID, key.datasetID)
		}
	}
	return nil
}

func (s *Service) observeTimeSeriesRows(ctx context.Context, rows []*pb.RowFieldUpsert, result string, committed, accepted bool) {
	if s == nil || s.metrics == nil {
		return
	}
	type aggregate struct {
		key       report.DatasetKey
		rows      uint64
		watermark time.Time
	}
	groups := make(map[report.DatasetKey]*aggregate)
	for _, row := range rows {
		if row == nil || row.GetKey() == nil {
			continue
		}
		key := row.GetKey()
		timeSeries := key.GetTimeSeries()
		if timeSeries == nil {
			continue
		}
		if s.metrics != nil {
			datasetKey := report.DatasetKey{
				SpaceID: key.GetSpaceId(), DatasetID: key.GetDatasetId(), Freq: timeSeries.GetFreq(),
			}
			item := groups[datasetKey]
			if item == nil {
				item = &aggregate{key: datasetKey}
				groups[datasetKey] = item
			}
			item.rows++
		}
		if !committed {
			continue
		}
		dataTime, err := time.Parse(time.RFC3339Nano, timeSeries.GetDataTime())
		if err != nil {
			if s.metrics != nil {
				log.WarnContextf(ctx, "storage dataset metrics skipped invalid data_time=%q: %v", timeSeries.GetDataTime(), err)
			}
			continue
		}
		if s.metrics != nil {
			datasetKey := report.DatasetKey{SpaceID: key.GetSpaceId(), DatasetID: key.GetDatasetId(), Freq: timeSeries.GetFreq()}
			if item := groups[datasetKey]; item.watermark.IsZero() || dataTime.After(item.watermark) {
				item.watermark = dataTime
			}
		}
	}
	finishedAt := time.Now().UTC()
	if s.metrics != nil {
		for _, item := range groups {
			observation := report.DatasetObservation{
				Key: item.key, Result: result, FinishedAt: finishedAt,
			}
			if committed {
				observation.Rows = item.rows
				observation.OutputWatermark = item.watermark
			}
			if err := s.metrics.ObserveRun(observation); err != nil {
				log.WarnContextf(ctx, "storage dataset metrics observe failed key=%+v result=%s: %v", item.key, result, err)
			}
		}
	}
}

// ReadRows 按精确键读取，是唯一的精确读取入口。只返回存在的行，顺序与请求一致（跳过不存在的），调用方按键匹配。
func (s *Service) ReadRows(ctx context.Context, req *pb.PrimaryReadRowsReq) (*pb.PrimaryReadRowsRsp, error) {
	if req == nil || len(req.GetKeys()) == 0 {
		return &pb.PrimaryReadRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("keys are required"))}, nil
	}
	if len(req.GetKeys()) > maxReadRowsKeys {
		return &pb.PrimaryReadRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, fmt.Errorf("keys exceed the limit of %d", maxReadRowsKeys))}, nil
	}
	if len(req.GetColumnNames()) == 0 && len(req.GetAttributeKeys()) == 0 {
		return &pb.PrimaryReadRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("column_names or attribute_keys are required"))}, nil
	}
	if err := s.authorizeRequest(req.GetAuthInfo()); err != nil {
		return &pb.PrimaryReadRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	ctx = s.requestContext(ctx)
	groups := make(map[routeKey][]*pb.RowKey)
	order := make([]routeKey, 0)
	for _, key := range req.GetKeys() {
		if key == nil || key.GetSpaceId() == "" || key.GetDatasetId() == "" {
			return &pb.PrimaryReadRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("row key is invalid"))}, nil
		}
		group := routeKey{spaceID: key.GetSpaceId(), datasetID: key.GetDatasetId()}
		if _, ok := groups[group]; !ok {
			order = append(order, group)
		}
		groups[group] = append(groups[group], key)
	}
	rowsByKey := make(map[string][]*pb.RowFieldValues, len(req.GetKeys()))
	latestRecordRows := make(map[string][]*pb.RowFieldValues)
	for _, group := range order {
		keys := groups[group]
		node, err := s.resolve(ctx, group.spaceID, group.datasetID)
		if err != nil {
			return nil, err
		}
		auth, err := s.signAuth(req.GetAuthInfo())
		if err != nil {
			return &pb.PrimaryReadRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
		}
		rsp, err := node.ReadRows(ctx, &pb.ReadRowsReq{AuthInfo: auth, DatasetId: group.datasetID, Keys: keys, ColumnNames: req.GetColumnNames(), AttributeKeys: req.GetAttributeKeys()})
		if err != nil {
			return nil, err
		}
		if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			return &pb.PrimaryReadRowsRsp{RetInfo: rsp.GetRetInfo()}, nil
		}
		for _, row := range rsp.GetRows() {
			id := rowKeyIdentity(row.GetKey())
			rowsByKey[id] = append(rowsByKey[id], row)
			if row.GetKey().GetRecord() != nil {
				latestID := latestRecordIdentity(row.GetKey())
				latestRecordRows[latestID] = append(latestRecordRows[latestID], row)
			}
		}
	}
	rows := make([]*pb.RowFieldValues, 0, len(req.GetKeys()))
	for _, key := range req.GetKeys() {
		if record := key.GetRecord(); record != nil && record.GetVersion() == "" {
			id := latestRecordIdentity(key)
			matches := latestRecordRows[id]
			if len(matches) != 0 {
				rows = append(rows, matches[0])
				latestRecordRows[id] = matches[1:]
			}
			continue
		}
		id := rowKeyIdentity(key)
		matches := rowsByKey[id]
		if len(matches) == 0 {
			continue
		}
		rows = append(rows, matches[0])
		rowsByKey[id] = matches[1:]
	}
	return &pb.PrimaryReadRowsRsp{RetInfo: retinfo.Success("success"), Rows: rows}, nil
}

const maxReadRowsKeys = 10000

func latestRecordIdentity(key *pb.RowKey) string {
	if key == nil || key.GetRecord() == nil {
		return ""
	}
	return key.GetSpaceId() + "\x00" + key.GetDatasetId() + "\x00" + key.GetRecord().GetRecordId()
}

func (s *Service) authorizeRequest(auth *pb.AuthInfo) error {
	if auth == nil {
		return errors.New("auth_info is required")
	}
	if s.authorize != nil {
		return s.authorize(auth)
	}
	return nil
}

func validateRow(ctx context.Context, row *pb.RowFieldUpsert, validator Validator) error {
	if row == nil || row.GetKey() == nil {
		return errors.New("row key is required")
	}
	key := row.GetKey()
	if strings.TrimSpace(key.GetSpaceId()) == "" || strings.TrimSpace(key.GetDatasetId()) == "" {
		return errors.New("space_id and dataset_id are required")
	}
	if key.GetTimeSeries() == nil && key.GetRecord() == nil {
		return errors.New("row key kind is required")
	}
	if record := key.GetRecord(); record != nil && strings.TrimSpace(record.GetVersion()) == "" {
		return errors.New("record version is required for writes")
	}
	if len(row.GetFields()) == 0 && len(row.GetAttributes()) == 0 {
		return errors.New("at least one field or attribute is required")
	}
	for _, field := range row.GetFields() {
		if field == nil || strings.TrimSpace(field.GetFieldId()) == "" || field.GetValue() == nil {
			return errors.New("field_id and value are required")
		}
	}
	for name, value := range row.GetAttributes() {
		if strings.TrimSpace(name) == "" || value == nil {
			return errors.New("attribute key and value are required")
		}
	}
	if validator != nil {
		return validator.ValidateRow(ctx, row)
	}
	return nil
}

func (s *Service) signAuth(auth *pb.AuthInfo) (*pb.AuthInfo, error) {
	if auth == nil {
		return nil, errors.New("auth_info is required")
	}
	if s.sign == nil {
		return auth, nil
	}
	return s.sign(auth)
}

func rowKeyIdentity(key *pb.RowKey) string {
	raw, _ := proto.MarshalOptions{Deterministic: true}.Marshal(key)
	return string(raw)
}

var _ pb.PrimaryStoreService = (*Service)(nil)

func (s *Service) String() string { return fmt.Sprintf("PrimaryStore[%p]", s) }
