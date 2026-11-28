package primarystore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	metacache "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/cache"
	"github.com/mooyang-code/moox/modules/storage/internal/storeerr"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/mooyang-code/moox/packages/seriesset"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/log"
)

// Store 是 PrimaryStore 读写的事实存储，即同进程的 Pebble 库。
type Store interface {
	UpsertFields(ctx context.Context, spaceID, datasetID string, rows []*pb.Row) error
	ReadRows(ctx context.Context, keys []*pb.RowKey, columns, attributeKeys []string) ([]*pb.Row, error)
	ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error)
	CountTimeSeriesRows(ctx context.Context, req *pb.CountTimeSeriesRowsReq) ([]*pb.SubjectRowCount, error)
	ReadDatasetChanges(ctx context.Context, req *pb.ReadDatasetChangesReq) (*pb.ReadDatasetChangesRsp, error)
	RegisterSeriesSnapshot(ctx context.Context, spaceID, datasetID, frequency string, series []seriesset.Series, effectiveFrom int64) (pebble.RegisterSnapshotResult, error)
	CommitTimeSeriesBatch(ctx context.Context, exp pebble.DatasetPeriodExpectation, items []pebble.TimeSeriesBatchItem) (pebble.DatasetPeriodResult, error)
	AppendDatasetMarker(ctx context.Context, raw []byte) (string, error)
	GetDatasetPeriodCompletedMarker(ctx context.Context, spaceID, datasetID, causeEventID string, periodTime int64) (*eventpb.EventMessage, bool, error)
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
	store     Store
	datasets  *metacache.DatasetCache
	metrics   DatasetRunObserver
	rejection DeadlineRejectionObserver
}

type Options struct {
	Store          Store
	Datasets       *metacache.DatasetCache
	DatasetMetrics DatasetRunObserver
	// DeadlineRejections 为空时不记录截止拒绝。
	DeadlineRejections DeadlineRejectionObserver
}

func New(opts Options) (*Service, error) {
	if opts.Store == nil {
		return nil, errors.New("store is required")
	}
	return &Service{
		store:     opts.Store,
		datasets:  opts.Datasets,
		metrics:   opts.DatasetMetrics,
		rejection: opts.DeadlineRejections,
	}, nil
}

// UpsertFields 写入一个数据集的行。每行的数据集必须与请求一致。
func (s *Service) UpsertFields(ctx context.Context, req *pb.PrimaryUpsertFieldsReq) (*pb.PrimaryUpsertFieldsRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetDatasetId()) == "" || len(req.GetRows()) == 0 {
		return &pb.PrimaryUpsertFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, dataset_id and rows are required"))}, nil
	}
	spaceID, datasetID := req.GetSpaceId(), req.GetDatasetId()
	rows := req.GetRows()
	for _, row := range rows {
		if row == nil || row.GetKey() == nil || row.GetKey().GetSpaceId() != spaceID || row.GetKey().GetDatasetId() != datasetID {
			return &pb.PrimaryUpsertFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("row dataset identity does not match request"))}, nil
		}
		if err := validateRowShape(row); err != nil {
			return &pb.PrimaryUpsertFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
		}
	}
	snapshot := s.datasets.Snapshot()
	dataset, err := activeDataset(snapshot, spaceID, datasetID)
	if err != nil {
		return &pb.PrimaryUpsertFieldsRsp{RetInfo: retinfo.FromError(err)}, nil
	}
	if err := validateRowsSchema(snapshot, dataset, rows); err != nil {
		return &pb.PrimaryUpsertFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	if err := s.store.UpsertFields(ctx, spaceID, datasetID, rows); err != nil {
		s.observeTimeSeriesRows(ctx, rows, "error", false)
		return &pb.PrimaryUpsertFieldsRsp{RetInfo: retinfo.FromError(err)}, nil
	}
	keys := make([]*pb.RowKey, 0, len(rows))
	for _, row := range rows {
		key, err := pebble.NormalizeRowKey(row.GetKey())
		if err != nil {
			return &pb.PrimaryUpsertFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
		}
		keys = append(keys, key)
	}
	s.observeTimeSeriesRows(ctx, rows, "success", true)
	return &pb.PrimaryUpsertFieldsRsp{RetInfo: retinfo.Success("success"), Keys: keys}, nil
}

// activeDataset 从本次请求快照中取出数据集并确认处于启用状态。
// 快照缺失归为内部错误（INNER_ERR，调用方可重试），数据集不存在归为 NOT_FOUND，未启用归为 INVALID_PARAM。
func activeDataset(snapshot *metacache.DatasetSnapshot, spaceID, datasetID string) (*pb.Dataset, error) {
	if snapshot == nil {
		return nil, errors.New("metadata cache snapshot is unavailable")
	}
	dataset, ok := snapshot.GetDataset(spaceID, datasetID)
	if !ok || dataset == nil {
		return nil, storeerr.NotFoundf("dataset %s/%s is not found", spaceID, datasetID)
	}
	if dataset.GetStatus() != "active" {
		return nil, storeerr.Invalidf("dataset %s/%s is not active", spaceID, datasetID)
	}
	return dataset, nil
}

func (s *Service) observeTimeSeriesRows(ctx context.Context, rows []*pb.Row, result string, committed bool) {
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
		datasetKey := report.DatasetKey{
			SpaceID: key.GetSpaceId(), DatasetID: key.GetDatasetId(), Freq: timeSeries.GetFrequency(),
		}
		item := groups[datasetKey]
		if item == nil {
			item = &aggregate{key: datasetKey}
			groups[datasetKey] = item
		}
		item.rows++
		if !committed {
			continue
		}
		dataTime, err := time.Parse(time.RFC3339Nano, timeSeries.GetDataTime())
		if err != nil {
			log.WarnContextf(ctx, "storage dataset metrics skipped invalid data_time=%q: %v", timeSeries.GetDataTime(), err)
			continue
		}
		if item.watermark.IsZero() || dataTime.After(item.watermark) {
			item.watermark = dataTime
		}
	}
	finishedAt := time.Now().UTC()
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
	snapshot := s.datasets.Snapshot()
	rowsByKey := make(map[string][]*pb.Row, len(req.GetKeys()))
	latestRecordRows := make(map[string][]*pb.Row)
	for _, group := range order {
		keys := groups[group]
		if _, err := activeDataset(snapshot, group.spaceID, group.datasetID); err != nil {
			return &pb.PrimaryReadRowsRsp{RetInfo: retinfo.FromError(err)}, nil
		}
		found, err := s.store.ReadRows(ctx, keys, req.GetColumnNames(), req.GetAttributeKeys())
		if err != nil {
			return &pb.PrimaryReadRowsRsp{RetInfo: retinfo.FromError(err)}, nil
		}
		for _, row := range found {
			id := rowKeyIdentity(row.GetKey())
			rowsByKey[id] = append(rowsByKey[id], row)
			if row.GetKey().GetRecord() != nil {
				latestID := latestRecordIdentity(row.GetKey())
				latestRecordRows[latestID] = append(latestRecordRows[latestID], row)
			}
		}
	}
	rows := make([]*pb.Row, 0, len(req.GetKeys()))
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

func rowKeyIdentity(key *pb.RowKey) string {
	raw, _ := proto.MarshalOptions{Deterministic: true}.Marshal(key)
	return string(raw)
}

var _ pb.PrimaryStoreService = (*Service)(nil)

func (s *Service) String() string { return fmt.Sprintf("PrimaryStore[%p]", s) }
