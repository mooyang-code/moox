//go:build legacy_storage

package view

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	"github.com/mooyang-code/moox/modules/storage/internal/typedvalue"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

var _ pb.DataViewService = (*Service)(nil)

const viewNotReadyMessage = "VIEW_NOT_READY: view schema change is building"

var errViewNotReady = errors.New(viewNotReadyMessage)

// Service implements DataView RPC APIs for materialized View stores.
//
// View 索引是可从 PrimaryStore 重建的有界派生结果；索引生命周期统一由
// viewbuilder 角色的 op=maintain 调度驱动，Service 只通过 owner 查询 active_index_id。
type Service struct {
	metadata          ViewMetadataReader
	timeSeriesIndexes TimeSeriesIndexQuery
	recordIndexes     RecordIndexQuery
	heads             ShardHeadReader
	timeSeriesStats   ViewIndexStatsReader
	recordStats       ViewIndexStatsReader
	requireFreshness  bool
}

type ViewMetadataReader interface {
	GetView(ctx context.Context, spaceID string, viewID string) (*pb.View, error)
	ListViewColumns(ctx context.Context, spaceID string, viewID string, page *pb.Page) ([]*pb.ViewColumn, *pb.PageResult, error)
	GetDataset(ctx context.Context, spaceID string, datasetID string) (*pb.Dataset, error)
}

type TimeSeriesIndexQuery interface {
	QueryTimeSeriesRows(ctx context.Context, indexID string, req *pb.QueryTimeSeriesRowsReq) ([]*pb.ResultColumn, []*pb.TimeSeriesRow, *pb.PageResult, error)
}

type RecordIndexQuery interface {
	QueryRecordRows(ctx context.Context, indexID string, datasetID string, req *pb.SearchRecordRowsReq) ([]*pb.ResultColumn, []*pb.RecordRow, *pb.PageResult, error)
}

// ViewIndexStatsReader lets the query owner verify the durable index checkpoint
// against the current PrimaryStore head.
type ViewIndexStatsReader interface {
	Stat(ctx context.Context, indexID string) (viewindex.ViewIndexStats, error)
}

type ServiceOptions struct {
	Metadata          ViewMetadataReader
	TimeSeriesIndexes TimeSeriesIndexQuery
	RecordIndexes     RecordIndexQuery
	Heads             ShardHeadReader
	RequireFreshness  bool
}

func NewService(opts ServiceOptions) *Service {
	return &Service{
		metadata: opts.Metadata, timeSeriesIndexes: opts.TimeSeriesIndexes, recordIndexes: opts.RecordIndexes,
		heads:           opts.Heads,
		timeSeriesStats: statsReader(opts.TimeSeriesIndexes), recordStats: statsReader(opts.RecordIndexes),
		requireFreshness: opts.RequireFreshness,
	}
}

func statsReader(candidate any) ViewIndexStatsReader {
	reader, _ := candidate.(ViewIndexStatsReader)
	return reader
}

func (s *Service) Close() error {
	return nil
}

func (s *Service) QueryTimeSeriesRows(ctx context.Context, req *pb.QueryTimeSeriesRowsReq) (*pb.QueryTimeSeriesRowsRsp, error) {
	if strings.TrimSpace(req.GetViewId()) == "" {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_VIEW_NOT_FOUND, errors.New("view_id is required"))}, nil
	}
	if err := ValidateTimeSeriesQueryOptions(req); err != nil {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	viewMeta, err := s.metadata.GetView(ctx, req.GetSpaceId(), req.GetViewId())
	if err != nil {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_VIEW_NOT_FOUND, err)}, nil
	}
	if err := s.validateTimeSeriesView(ctx, viewMeta); err != nil {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	if viewMeta.GetActiveIndexId() == "" {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_VIEW_NOT_FOUND, errors.New("view active_index_id is empty"))}, nil
	}
	complete, err := s.validateTimeSeriesFreshness(ctx, req, viewMeta)
	if err != nil {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_VIEW_NOT_READY, err)}, nil
	}
	if err := s.validateTimeSeriesActiveSchema(ctx, viewMeta, requestedViewQueryFields(req.GetColumnNames(), req.GetFilters(), req.GetSorts())); err != nil {
		if IsViewNotReadyError(err) {
			return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_VIEW_NOT_READY, err)}, nil
		}
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	if s.timeSeriesIndexes == nil {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errors.New("time series index owner is required"))}, nil
	}
	columns, rows, page, err := s.timeSeriesIndexes.QueryTimeSeriesRows(ctx, viewMeta.GetActiveIndexId(), req)
	if err != nil {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Success("success"), Columns: columns, Rows: rows, PageResult: page, ServedIndexedFrom: viewMeta.GetIndexedFrom(), ServedIndexedTo: viewMeta.GetIndexedTo(), Complete: complete}, nil
}

func (s *Service) SearchRecordRows(ctx context.Context, req *pb.SearchRecordRowsReq) (*pb.SearchRecordRowsRsp, error) {
	if strings.TrimSpace(req.GetViewId()) == "" {
		return &pb.SearchRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("view_id is required"))}, nil
	}
	viewMeta, err := s.metadata.GetView(ctx, req.GetSpaceId(), req.GetViewId())
	if err != nil {
		return &pb.SearchRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_VIEW_NOT_FOUND, err)}, nil
	}
	if err := s.validateRecordView(ctx, viewMeta); err != nil {
		return &pb.SearchRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	if viewMeta.GetActiveIndexId() == "" {
		return &pb.SearchRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_VIEW_NOT_FOUND, errors.New("view active_index_id is empty"))}, nil
	}
	complete, err := s.validateRecordFreshness(ctx, req, viewMeta)
	if err != nil {
		return &pb.SearchRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_VIEW_NOT_READY, err)}, nil
	}
	datasetID := strings.TrimSpace(viewMeta.GetPrimaryDatasetId())
	if datasetID == "" {
		return &pb.SearchRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("view primary_dataset_id is required"))}, nil
	}
	if s.recordIndexes == nil {
		return &pb.SearchRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errors.New("record index owner is required"))}, nil
	}
	latestColumns, _, err := s.metadata.ListViewColumns(ctx, req.GetSpaceId(), req.GetViewId(), &pb.Page{Size: 10000})
	if err != nil {
		return &pb.SearchRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	requestedFields := requestedViewQueryFields(req.GetColumnNames(), req.GetFilters(), req.GetSorts())
	if err := ValidateRequestedColumnsAgainstActiveSchema(viewMeta, requestedFields, ViewColumnNames(viewMeta.GetActiveColumns()), latestColumns); err != nil {
		if IsViewNotReadyError(err) {
			return &pb.SearchRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_VIEW_NOT_READY, err)}, nil
		}
		return &pb.SearchRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	query := proto.Clone(req).(*pb.SearchRecordRowsReq)
	query.Keys, err = normalizeRecordSearchKeys(req.GetSpaceId(), datasetID, req.GetKeys())
	if err != nil {
		return &pb.SearchRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	_, rows, page, err := s.recordIndexes.QueryRecordRows(ctx, viewMeta.GetActiveIndexId(), datasetID, query)
	if err != nil {
		return &pb.SearchRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	for idx, row := range rows {
		rows[idx] = projectRecordRowColumns(row, req.GetColumnNames())
	}
	return &pb.SearchRecordRowsRsp{
		RetInfo: retinfo.Success("success"), Columns: projectResultColumns(viewMeta.GetActiveColumns(), req.GetColumnNames()), Rows: rows, PageResult: page, ServedIndexedFrom: viewMeta.GetIndexedFrom(), ServedIndexedTo: viewMeta.GetIndexedTo(), Complete: complete,
	}, nil
}

func (s *Service) validateTimeSeriesActiveSchema(ctx context.Context, viewMeta *pb.View, requested []string) error {
	if !NeedsActiveSchemaValidation(viewMeta, requested) {
		return nil
	}
	latestColumns, _, err := s.metadata.ListViewColumns(ctx, viewMeta.GetSpaceId(), viewMeta.GetViewId(), &pb.Page{Size: 10000})
	if err != nil {
		return err
	}
	activeNames := ViewColumnNames(viewMeta.GetActiveColumns())
	return ValidateRequestedColumnsAgainstActiveSchema(viewMeta, requested, activeNames, latestColumns)
}

func NeedsActiveSchemaValidation(viewMeta *pb.View, requested []string) bool {
	return viewMeta != nil && viewMeta.GetViewVersion() > viewMeta.GetActiveViewVersion() && len(requested) > 0
}

func ValidateRequestedColumnsAgainstActiveSchema(viewMeta *pb.View, requested []string, activeNames []string, latestColumns []*pb.ViewColumn) error {
	if !NeedsActiveSchemaValidation(viewMeta, requested) {
		return nil
	}
	active := typedvalue.StringSet(activeNames)
	latest := map[string]bool{}
	for _, column := range latestColumns {
		name := strings.TrimSpace(column.GetColumnName())
		if name != "" {
			latest[name] = true
		}
	}
	for _, name := range requested {
		name = strings.TrimSpace(name)
		if name == "" || active[name] {
			continue
		}
		if latest[name] {
			return errViewNotReady
		}
	}
	return nil
}

func IsViewNotReadyError(err error) bool {
	return errors.Is(err, errViewNotReady)
}

func ViewColumnNames(columns []*pb.ViewColumn) []string {
	out := make([]string, 0, len(columns))
	for _, column := range columns {
		if name := strings.TrimSpace(column.GetColumnName()); name != "" {
			out = append(out, name)
		}
	}
	return out
}

func requestedViewQueryFields(columns []string, filters []*pb.FilterExpr, sorts []*pb.SortSpec) []string {
	seen := make(map[string]bool, len(columns)+len(filters)+len(sorts))
	out := make([]string, 0, len(seen))
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	for _, name := range columns {
		add(name)
	}
	for _, filter := range filters {
		add(viewFilterField(filter.GetExpr()))
	}
	for _, sortSpec := range sorts {
		add(sortSpec.GetFieldName())
	}
	return out
}

func viewFilterField(expr string) string {
	expr = strings.TrimSpace(expr)
	if open := strings.Index(expr, "("); open > 0 && strings.HasSuffix(expr, ")") {
		body := strings.TrimSpace(strings.TrimSuffix(expr[open+1:], ")"))
		field, _, _ := strings.Cut(body, ",")
		return strings.TrimSpace(field)
	}
	for _, operator := range []string{" contains ", "==", "!=", ">=", "<=", "=", ">", "<"} {
		if index := strings.Index(expr, operator); index >= 0 {
			return strings.TrimSpace(expr[:index])
		}
	}
	return ""
}

func (s *Service) validateTimeSeriesView(ctx context.Context, viewMeta *pb.View) error {
	if viewMeta == nil {
		return errors.New("view is required")
	}
	if !strings.EqualFold(strings.TrimSpace(viewMeta.GetEngine()), "duckdb") {
		return errors.New("time series view requires duckdb engine")
	}
	dataset, err := s.metadata.GetDataset(ctx, viewMeta.GetSpaceId(), viewMeta.GetPrimaryDatasetId())
	if err != nil {
		return err
	}
	if dataset.GetDataKind() != pb.DataKind_DATA_KIND_TIME_SERIES {
		return errors.New("time series view requires time series primary dataset")
	}
	return nil
}

func (s *Service) validateTimeSeriesFreshness(ctx context.Context, req *pb.QueryTimeSeriesRowsReq, viewMeta *pb.View) (bool, error) {
	if status := strings.ToLower(strings.TrimSpace(viewMeta.GetStatus())); status != "" && status != "active" {
		return false, errors.New("view is inactive")
	}
	dataset, err := s.metadata.GetDataset(ctx, viewMeta.GetSpaceId(), viewMeta.GetPrimaryDatasetId())
	if err != nil {
		return false, err
	}
	if status := strings.ToLower(strings.TrimSpace(dataset.GetStatus())); status != "" && status != "active" {
		return false, errors.New("dataset is inactive")
	}
	start, end := strings.TrimSpace(viewMeta.GetIndexedFrom()), strings.TrimSpace(viewMeta.GetIndexedTo())
	if (start == "") != (end == "") {
		return false, errors.New("view indexed range must provide both indexed_from and indexed_to")
	}
	for name, value := range map[string]string{"indexed_from": start, "indexed_to": end} {
		if value != "" {
			if _, ok := typedvalue.ParseTime(value); !ok {
				return false, fmt.Errorf("%s is not canonical RFC3339 time", name)
			}
		}
	}
	if start != "" || end != "" {
		queryStart, queryEnd := "", ""
		if req.GetTimeRange() != nil {
			queryStart, queryEnd = req.GetTimeRange().GetStartTime(), req.GetTimeRange().GetEndTime()
		}
		if queryStart == "" || queryEnd == "" {
			return false, errors.New("query time range is required for a bounded view")
		}
		if queryStart != "" && start != "" && compareRFC3339(queryStart, start) < 0 {
			return false, errors.New("query starts before indexed_from")
		}
		if queryEnd != "" && end != "" && compareRFC3339(queryEnd, end) > 0 {
			return false, errors.New("query ends after indexed_to")
		}
	}
	return s.validateIndexFreshness(ctx, viewMeta, s.timeSeriesStats)
}

func (s *Service) validateRecordFreshness(ctx context.Context, req *pb.SearchRecordRowsReq, viewMeta *pb.View) (bool, error) {
	if status := strings.ToLower(strings.TrimSpace(viewMeta.GetStatus())); status != "" && status != "active" {
		return false, errors.New("view is inactive")
	}
	dataset, err := s.metadata.GetDataset(ctx, viewMeta.GetSpaceId(), viewMeta.GetPrimaryDatasetId())
	if err != nil {
		return false, err
	}
	if status := strings.ToLower(strings.TrimSpace(dataset.GetStatus())); status != "" && status != "active" {
		return false, errors.New("dataset is inactive")
	}
	start, end := strings.TrimSpace(viewMeta.GetIndexedFrom()), strings.TrimSpace(viewMeta.GetIndexedTo())
	if (start == "") != (end == "") {
		return false, errors.New("view indexed range must provide both indexed_from and indexed_to")
	}
	if versionRange := req.GetVersionRange(); versionRange != nil {
		if (start != "" || end != "") && (versionRange.GetStartVersion() == "" || versionRange.GetEndVersion() == "") {
			return false, errors.New("query version range is required for a bounded view")
		}
		if versionRange.GetStartVersion() != "" && start != "" && strings.Compare(versionRange.GetStartVersion(), start) < 0 {
			return false, errors.New("query starts before indexed_from")
		}
		if versionRange.GetEndVersion() != "" && end != "" && strings.Compare(versionRange.GetEndVersion(), end) > 0 {
			return false, errors.New("query ends after indexed_to")
		}
	} else if start != "" || end != "" {
		return false, errors.New("query version range is required for a bounded view")
	}
	return s.validateIndexFreshness(ctx, viewMeta, s.recordStats)
}

func (s *Service) validateIndexFreshness(ctx context.Context, viewMeta *pb.View, statsReader ViewIndexStatsReader) (bool, error) {
	attrs := viewMeta.GetAttributes()
	if !s.requireFreshness && attrs["checkpoint_sequence"] != "" && attrs["shard_head_sequence"] != "" {
		if attrs["checkpoint_sequence"] != attrs["shard_head_sequence"] {
			return false, errors.New("view checkpoint is behind shard head")
		}
		return true, nil
	}
	if s.heads == nil || statsReader == nil {
		if !s.requireFreshness {
			return false, nil
		}
		return false, errors.New("view freshness cannot be verified")
	}
	stats, err := statsReader.Stat(ctx, viewMeta.GetActiveIndexId())
	if err != nil {
		return false, fmt.Errorf("read view index freshness: %w", err)
	}
	heads, err := shardHeadsForView(ctx, s.heads, viewMeta)
	if err != nil {
		return false, fmt.Errorf("read primary shard freshness: %w", err)
	}
	if len(heads) == 0 {
		return false, errors.New("primary shard freshness is unavailable")
	}
	if len(stats.ShardCheckpoints) != len(heads) {
		return false, errors.New("view shard checkpoint set does not match primary shard set")
	}
	for shardID, head := range heads {
		checkpoint, ok := stats.ShardCheckpoints[shardID]
		if !ok || checkpoint != head {
			return false, errors.New("view checkpoint is behind shard head")
		}
	}
	for shardID := range stats.ShardCheckpoints {
		if _, ok := heads[shardID]; !ok {
			return false, errors.New("view shard checkpoint set does not match primary shard set")
		}
	}
	return true, nil
}

func compareRFC3339(left, right string) int {
	l, lok := typedvalue.ParseTime(left)
	r, rok := typedvalue.ParseTime(right)
	if lok && rok {
		if l.Before(r) {
			return -1
		}
		if l.After(r) {
			return 1
		}
		return 0
	}
	return strings.Compare(left, right)
}

func viewFreshnessConfirmed(viewMeta *pb.View) bool {
	if viewMeta == nil {
		return false
	}
	attrs := viewMeta.GetAttributes()
	checkpoint, head := strings.TrimSpace(attrs["checkpoint_sequence"]), strings.TrimSpace(attrs["shard_head_sequence"])
	return checkpoint != "" && checkpoint == head
}

func (s *Service) validateRecordView(ctx context.Context, viewMeta *pb.View) error {
	if viewMeta == nil {
		return errors.New("view is required")
	}
	if !strings.EqualFold(strings.TrimSpace(viewMeta.GetEngine()), "bleve") {
		return errors.New("record view requires bleve engine")
	}
	dataset, err := s.metadata.GetDataset(ctx, viewMeta.GetSpaceId(), viewMeta.GetPrimaryDatasetId())
	if err != nil {
		return err
	}
	if dataset.GetDataKind() != pb.DataKind_DATA_KIND_RECORD {
		return errors.New("record view requires record primary dataset")
	}
	return nil
}

func normalizeRecordSearchKeys(spaceID string, datasetID string, keys []*pb.RecordKey) ([]*pb.RecordKey, error) {
	if strings.TrimSpace(spaceID) == "" || strings.TrimSpace(datasetID) == "" {
		return nil, errors.New("space_id and dataset_id are required")
	}
	out := make([]*pb.RecordKey, 0, len(keys))
	for _, key := range keys {
		if key == nil {
			continue
		}
		copied := proto.Clone(key).(*pb.RecordKey)
		if copied.GetSpaceId() == "" {
			copied.SpaceId = spaceID
		}
		if copied.GetDatasetId() == "" {
			copied.DatasetId = datasetID
		}
		if copied.GetSpaceId() != spaceID || copied.GetDatasetId() != datasetID {
			return nil, errors.New("record key must belong to the query view primary dataset")
		}
		if copied.GetRecordId() != "" {
			out = append(out, copied)
		}
	}
	return out, nil
}

func projectRecordRowColumns(row *pb.RecordRow, includes []string) *pb.RecordRow {
	if len(includes) == 0 {
		return row
	}
	allow := make(map[string]bool, len(includes))
	for _, name := range includes {
		allow[name] = true
	}
	filtered := proto.Clone(row).(*pb.RecordRow)
	filtered.Columns = filtered.Columns[:0]
	for _, column := range row.GetColumns() {
		if allow[column.GetColumnName()] {
			filtered.Columns = append(filtered.Columns, column)
		}
	}
	return filtered
}

func projectResultColumns(columns []*pb.ViewColumn, includes []string) []*pb.ResultColumn {
	allow := map[string]bool(nil)
	if len(includes) > 0 {
		allow = make(map[string]bool, len(includes))
		for _, name := range includes {
			allow[name] = true
		}
	}
	out := make([]*pb.ResultColumn, 0, len(columns))
	for _, column := range columns {
		if allow != nil && !allow[column.GetColumnName()] {
			continue
		}
		out = append(out, &pb.ResultColumn{ColumnName: column.GetColumnName(), OriginType: column.GetOriginType(), DatasetId: viewColumnDatasetID(column), OriginId: column.GetOriginId(), ValueType: column.GetValueType()})
	}
	return out
}

func viewColumnDatasetID(column *pb.ViewColumn) string {
	originID := column.GetOriginId()
	if before, _, ok := strings.Cut(originID, "."); ok {
		return before
	}
	return ""
}
