package view

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"

	"github.com/mooyang-code/moox/modules/storage/internal/core/factvalue"
	"github.com/mooyang-code/moox/modules/storage/internal/core/response"
	deviceduckdb "github.com/mooyang-code/moox/modules/storage/internal/infra/device/duckdb"
	"github.com/mooyang-code/moox/modules/storage/internal/infra/device/factkey"
	"github.com/mooyang-code/moox/modules/storage/internal/services/view/search"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/rs/xid"
	"google.golang.org/protobuf/proto"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

var _ pb.DataViewService = (*Service)(nil)

// Service implements DataView RPC APIs for materialized View stores.
type Service struct {
	metadata Metadata
	views    *deviceduckdb.ViewStore
	search   *search.Service
	facts    FactReader
	records  RecordFactReader
	builder  *Builder

	asyncMu sync.Mutex
	asyncWG sync.WaitGroup
	closing bool
}

type ServiceOptions struct {
	Metadata Metadata
	Views    *deviceduckdb.ViewStore
	Search   *search.Service
	Facts    FactReader
	Records  RecordFactReader
	Builder  *Builder
}

func NewService(opts ServiceOptions) *Service {
	records := opts.Records
	if records == nil {
		if reader, ok := opts.Facts.(RecordFactReader); ok {
			records = reader
		}
	}
	builder := opts.Builder
	if builder == nil {
		builder = NewBuilder(Options{Metadata: opts.Metadata, Facts: opts.Facts, Records: records, Views: opts.Views, Search: opts.Search})
	}
	return &Service{metadata: opts.Metadata, views: opts.Views, search: opts.Search, facts: opts.Facts, records: records, builder: builder}
}

func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	s.asyncMu.Lock()
	s.closing = true
	s.asyncMu.Unlock()
	s.asyncWG.Wait()
	return nil
}

func (s *Service) QueryTimeSeriesRows(ctx context.Context, req *pb.QueryTimeSeriesRowsReq) (*pb.QueryTimeSeriesRowsRsp, error) {
	if strings.TrimSpace(req.GetViewId()) == "" {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_VIEW_NOT_FOUND, errors.New("view_id is required"))}, nil
	}
	viewMeta, err := s.metadata.GetView(ctx, req.GetSpaceId(), req.GetViewId())
	if err != nil {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_VIEW_NOT_FOUND, err)}, nil
	}
	if err := s.validateTimeSeriesView(ctx, viewMeta); err != nil {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	if viewMeta.GetActiveResult() == "" {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_VIEW_NOT_FOUND, errors.New("view active_result is empty"))}, nil
	}
	if s.views == nil {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_INNER_ERR, errors.New("view store is required"))}, nil
	}
	columns, rows, page, err := s.views.QueryTimeSeriesRows(ctx, viewMeta.GetActiveResult(), req)
	if err != nil {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	return &pb.QueryTimeSeriesRowsRsp{RetInfo: response.Success("success"), Columns: columns, Rows: rows, PageResult: page}, nil
}

func (s *Service) SearchRecordRows(ctx context.Context, req *pb.SearchRecordRowsReq) (*pb.SearchRecordRowsRsp, error) {
	if strings.TrimSpace(req.GetViewId()) == "" {
		return &pb.SearchRecordRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("view_id is required"))}, nil
	}
	viewMeta, err := s.metadata.GetView(ctx, req.GetSpaceId(), req.GetViewId())
	if err != nil {
		return &pb.SearchRecordRowsRsp{RetInfo: response.Error(pb.ErrorCode_VIEW_NOT_FOUND, err)}, nil
	}
	if err := s.validateRecordView(ctx, viewMeta); err != nil {
		return &pb.SearchRecordRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	if viewMeta.GetActiveResult() == "" {
		return &pb.SearchRecordRowsRsp{RetInfo: response.Error(pb.ErrorCode_VIEW_NOT_FOUND, errors.New("view active_result is empty"))}, nil
	}
	datasetID := viewMeta.GetPrimaryDatasetId()
	if strings.TrimSpace(datasetID) == "" {
		return &pb.SearchRecordRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("view primary_dataset_id is required"))}, nil
	}
	viewColumns, _, err := s.metadata.ListViewColumns(ctx, req.GetSpaceId(), req.GetViewId(), &pb.Page{Size: 10000})
	if err != nil {
		return &pb.SearchRecordRowsRsp{RetInfo: response.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	keys, err := normalizeRecordSearchKeys(req.GetSpaceId(), datasetID, req.GetKeys())
	if err != nil {
		return &pb.SearchRecordRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	recordIDs := make([]string, 0, len(keys))
	for _, key := range keys {
		recordIDs = append(recordIDs, key.GetRecordId())
	}
	if s.search == nil {
		return &pb.SearchRecordRowsRsp{RetInfo: response.Error(pb.ErrorCode_INNER_ERR, errors.New("search service is required"))}, nil
	}
	rows, _, err := s.search.SearchRecordRows(ctx, search.SearchRequest{
		ResultName:   viewMeta.GetActiveResult(),
		SpaceID:      req.GetSpaceId(),
		DatasetID:    datasetID,
		RecordIDs:    recordIDs,
		TextQuery:    req.GetTextQuery(),
		VersionRange: req.GetVersionRange(),
	})
	if err != nil {
		return &pb.SearchRecordRowsRsp{RetInfo: response.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	var matched []*pb.RecordRow
	for _, row := range rows {
		if recordRowMatchesSearchKeys(row, keys, req.GetVersionRange() != nil) && recordRowMatchesFilters(row, req.GetFilters()) {
			matched = append(matched, projectRecordRowColumns(row, req.GetColumnNames()))
		}
	}
	sortSearchRecordRows(matched, req.GetSorts())
	paged, page := pageRecordRows(matched, req.GetPage())
	return &pb.SearchRecordRowsRsp{RetInfo: response.Success("success"), Columns: projectResultColumns(viewColumns, req.GetColumnNames()), Rows: paged, PageResult: page}, nil
}

func (s *Service) RebuildTimeSeriesView(ctx context.Context, req *pb.RebuildTimeSeriesViewReq) (*pb.RebuildTimeSeriesViewRsp, error) {
	if strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetViewId()) == "" {
		return &pb.RebuildTimeSeriesViewRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and view_id are required"))}, nil
	}
	viewMeta, err := s.metadata.GetView(ctx, req.GetSpaceId(), req.GetViewId())
	if err != nil {
		return &pb.RebuildTimeSeriesViewRsp{RetInfo: response.Error(pb.ErrorCode_VIEW_NOT_FOUND, err)}, nil
	}
	if err := s.validateTimeSeriesView(ctx, viewMeta); err != nil {
		return &pb.RebuildTimeSeriesViewRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	rebuildID := xid.New().String()
	rebuildReq := proto.Clone(req).(*pb.RebuildTimeSeriesViewReq)
	if !s.acceptAsync() {
		return &pb.RebuildTimeSeriesViewRsp{RetInfo: response.Error(pb.ErrorCode_INNER_ERR, errors.New("service is closing"))}, nil
	}
	go func() {
		defer s.asyncWG.Done()
		asyncCtx := trpc.CloneContext(ctx)
		if _, err := s.builder.Build(asyncCtx, rebuildReq.GetSpaceId(), rebuildReq.GetViewId()); err != nil {
			log.ErrorContextf(asyncCtx, "[ViewService] time_series_view_rebuild failed: %v", err)
		}
	}()
	return &pb.RebuildTimeSeriesViewRsp{RetInfo: response.Success("rebuild accepted"), RebuildId: rebuildID}, nil
}

func (s *Service) RebuildRecordView(ctx context.Context, req *pb.RebuildRecordViewReq) (*pb.RebuildRecordViewRsp, error) {
	if strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetViewId()) == "" {
		return &pb.RebuildRecordViewRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and view_id are required"))}, nil
	}
	viewMeta, err := s.metadata.GetView(ctx, req.GetSpaceId(), req.GetViewId())
	if err != nil {
		return &pb.RebuildRecordViewRsp{RetInfo: response.Error(pb.ErrorCode_VIEW_NOT_FOUND, err)}, nil
	}
	if err := s.validateRecordView(ctx, viewMeta); err != nil {
		return &pb.RebuildRecordViewRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	rebuildID := xid.New().String()
	rebuildReq := proto.Clone(req).(*pb.RebuildRecordViewReq)
	if !s.acceptAsync() {
		return &pb.RebuildRecordViewRsp{RetInfo: response.Error(pb.ErrorCode_INNER_ERR, errors.New("service is closing"))}, nil
	}
	go func() {
		defer s.asyncWG.Done()
		asyncCtx := trpc.CloneContext(ctx)
		if _, err := s.builder.Build(asyncCtx, rebuildReq.GetSpaceId(), rebuildReq.GetViewId()); err != nil {
			log.ErrorContextf(asyncCtx, "[ViewService] record_view_rebuild failed: %v", err)
		}
	}()
	return &pb.RebuildRecordViewRsp{RetInfo: response.Success("rebuild accepted"), RebuildId: rebuildID}, nil
}

func (s *Service) acceptAsync() bool {
	s.asyncMu.Lock()
	defer s.asyncMu.Unlock()
	if s.closing {
		return false
	}
	s.asyncWG.Add(1)
	return true
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

func recordRowMatchesSearchKeys(row *pb.RecordRow, keys []*pb.RecordKey, versionRangeMode bool) bool {
	if len(keys) == 0 {
		return true
	}
	if row == nil || row.GetKey() == nil {
		return false
	}
	rowKey := row.GetKey()
	for _, key := range keys {
		if key.GetRecordId() != rowKey.GetRecordId() {
			continue
		}
		if recordVersionMatches(rowKey.GetVersion(), key.GetVersion(), versionRangeMode) {
			return true
		}
	}
	return false
}

func recordVersionMatches(rowVersion string, wantVersion string, versionRangeMode bool) bool {
	rowVersion = strings.TrimSpace(rowVersion)
	wantVersion = strings.TrimSpace(wantVersion)
	if wantVersion == "" {
		if versionRangeMode {
			return true
		}
		return rowVersion == "" || rowVersion == factkey.EmptyVersion
	}
	return factkey.NormalizeVersion(rowVersion) == factkey.NormalizeVersion(wantVersion)
}

func recordRowMatchesFilters(row *pb.RecordRow, filters []*pb.FilterExpr) bool {
	for _, filter := range filters {
		if !recordRowMatchesFilter(row, filter) {
			return false
		}
	}
	return true
}

func recordRowMatchesFilter(row *pb.RecordRow, filter *pb.FilterExpr) bool {
	if filter == nil || strings.TrimSpace(filter.GetExpr()) == "" {
		return true
	}
	if fn, field, token, ok := parseFunctionFilter(filter.GetExpr()); ok {
		rowValue, exists := recordRowValue(row, field)
		switch fn {
		case "is_empty":
			return !exists || factvalue.String(rowValue) == ""
		case "is_not_empty":
			return exists && factvalue.String(rowValue) != ""
		}
		if !exists {
			return fn == "not_contains"
		}
		expected := filterValue(token, filter.GetArgs())
		if expected == nil {
			return false
		}
		left := factvalue.String(rowValue)
		right := factvalue.String(expected)
		switch fn {
		case "starts_with":
			return strings.HasPrefix(left, right)
		case "ends_with":
			return strings.HasSuffix(left, right)
		case "not_contains":
			return !strings.Contains(left, right)
		default:
			return false
		}
	}
	left, op, right, ok := parseSimpleFilter(filter.GetExpr())
	if !ok {
		return false
	}
	rowValue, ok := recordRowValue(row, left)
	if !ok {
		return false
	}
	expected := filterValue(right, filter.GetArgs())
	if expected == nil {
		return false
	}
	return compareTypedValues(rowValue, expected, op)
}

func parseSimpleFilter(expr string) (left, op, right string, ok bool) {
	expr = strings.TrimSpace(expr)
	for _, candidate := range []string{" contains ", "==", "!=", ">=", "<=", "=", ">", "<"} {
		if idx := strings.Index(expr, candidate); idx >= 0 {
			left = strings.TrimSpace(expr[:idx])
			right = strings.TrimSpace(expr[idx+len(candidate):])
			op = strings.TrimSpace(candidate)
			if left == "" || right == "" {
				return "", "", "", false
			}
			return left, op, right, true
		}
	}
	return "", "", "", false
}

func parseFunctionFilter(expr string) (name, field, token string, ok bool) {
	expr = strings.TrimSpace(expr)
	open := strings.Index(expr, "(")
	if open <= 0 || !strings.HasSuffix(expr, ")") {
		return "", "", "", false
	}
	name = strings.TrimSpace(expr[:open])
	body := strings.TrimSpace(strings.TrimSuffix(expr[open+1:], ")"))
	if name == "" || body == "" {
		return "", "", "", false
	}
	switch name {
	case "is_empty", "is_not_empty":
		if strings.Contains(body, ",") {
			return "", "", "", false
		}
		return name, strings.TrimSpace(body), "", true
	case "starts_with", "ends_with", "not_contains":
		left, right, found := strings.Cut(body, ",")
		if !found {
			return "", "", "", false
		}
		field = strings.TrimSpace(left)
		token = strings.TrimSpace(right)
		if field == "" || token == "" {
			return "", "", "", false
		}
		return name, field, token, true
	default:
		return "", "", "", false
	}
}

func filterValue(token string, args map[string]*pb.TypedValue) *pb.TypedValue {
	token = strings.TrimSpace(token)
	if strings.HasPrefix(token, "$") {
		return args[strings.TrimPrefix(token, "$")]
	}
	if strings.HasPrefix(token, "'") && strings.HasSuffix(token, "'") && len(token) >= 2 {
		return &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: strings.Trim(token, "'")}}
	}
	if strings.HasPrefix(token, `"`) && strings.HasSuffix(token, `"`) && len(token) >= 2 {
		return &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: strings.Trim(token, `"`)}}
	}
	return nil
}

func recordRowColumnValue(row *pb.RecordRow, name string) (*pb.TypedValue, bool) {
	for _, column := range row.GetColumns() {
		if column.GetColumnName() == name {
			return column.GetValue(), true
		}
	}
	return nil, false
}

func recordRowValue(row *pb.RecordRow, name string) (*pb.TypedValue, bool) {
	if row == nil {
		return nil, false
	}
	key := row.GetKey()
	switch name {
	case "record_id":
		if key.GetRecordId() == "" {
			return nil, false
		}
		return &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: key.GetRecordId()}}, true
	case "version":
		if key.GetVersion() == "" {
			return nil, false
		}
		return &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: key.GetVersion()}}, true
	default:
		return recordRowColumnValue(row, name)
	}
}

func compareTypedValues(left, right *pb.TypedValue, op string) bool {
	if op == "contains" {
		return strings.Contains(factvalue.String(left), factvalue.String(right))
	}
	cmp := factvalue.Compare(left, right)
	switch op {
	case "=", "==":
		return cmp == 0
	case "!=":
		return cmp != 0
	case ">":
		return cmp > 0
	case ">=":
		return cmp >= 0
	case "<":
		return cmp < 0
	case "<=":
		return cmp <= 0
	default:
		return false
	}
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

func sortSearchRecordRows(rows []*pb.RecordRow, sorts []*pb.SortSpec) {
	if len(sorts) == 0 {
		sort.SliceStable(rows, func(i, j int) bool {
			left := rows[i].GetKey()
			right := rows[j].GetKey()
			if left.GetRecordId() == right.GetRecordId() {
				return left.GetVersion() < right.GetVersion()
			}
			return left.GetRecordId() < right.GetRecordId()
		})
		return
	}
	sort.SliceStable(rows, func(i, j int) bool {
		for _, spec := range sorts {
			left, _ := recordRowValue(rows[i], spec.GetFieldName())
			right, _ := recordRowValue(rows[j], spec.GetFieldName())
			leftText := factvalue.String(left)
			rightText := factvalue.String(right)
			if leftText == rightText {
				continue
			}
			if spec.GetDesc() {
				return leftText > rightText
			}
			return leftText < rightText
		}
		left := rows[i].GetKey()
		right := rows[j].GetKey()
		if left.GetRecordId() == right.GetRecordId() {
			return left.GetVersion() < right.GetVersion()
		}
		return left.GetRecordId() < right.GetRecordId()
	})
}

func pageRecordRows(rows []*pb.RecordRow, page *pb.Page) ([]*pb.RecordRow, *pb.PageResult) {
	if page == nil || page.GetSize() == 0 {
		return rows, &pb.PageResult{Page: 1, Size: uint32(len(rows)), Total: uint32(len(rows)), HasMore: false}
	}
	pageNo := page.GetPage()
	if pageNo == 0 {
		pageNo = 1
	}
	size := page.GetSize()
	start := int((pageNo - 1) * size)
	if start >= len(rows) {
		return nil, &pb.PageResult{Page: pageNo, Size: size, Total: uint32(len(rows)), HasMore: false}
	}
	end := start + int(size)
	if end > len(rows) {
		end = len(rows)
	}
	return rows[start:end], &pb.PageResult{Page: pageNo, Size: size, Total: uint32(len(rows)), HasMore: end < len(rows)}
}
