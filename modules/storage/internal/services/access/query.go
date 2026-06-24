package access

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/core/factvalue"
	"github.com/mooyang-code/moox/modules/storage/internal/core/response"
	"github.com/mooyang-code/moox/modules/storage/internal/infra/device/factkey"
	searchsvc "github.com/mooyang-code/moox/modules/storage/internal/services/search"
	"github.com/mooyang-code/moox/modules/storage/internal/services/view"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/rs/xid"
	"google.golang.org/protobuf/proto"
)

const rebuildViewPageSize = 1000

func (s *Service) QueryTimeSeriesRows(ctx context.Context, req *pb.QueryTimeSeriesRowsReq) (*pb.QueryTimeSeriesRowsRsp, error) {
	if req.GetViewId() == "" {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_VIEW_NOT_FOUND, errText("view_id is required"))}, nil
	}
	view, err := s.metadataReader.GetView(ctx, req.GetSpaceId(), req.GetViewId())
	if err != nil {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_VIEW_NOT_FOUND, err)}, nil
	}
	if err := s.validateTimeSeriesView(ctx, view); err != nil {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	if view.GetActiveResult() == "" {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_VIEW_NOT_FOUND, errText("view active_result is empty"))}, nil
	}
	viewStore, err := s.viewStore()
	if err != nil {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	columns, rows, page, err := viewStore.QueryTimeSeriesRows(ctx, view.GetActiveResult(), req)
	if err != nil {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	return &pb.QueryTimeSeriesRowsRsp{RetInfo: response.Success("success"), Columns: columns, Rows: rows, PageResult: page}, nil
}

func (s *Service) SearchRecordRows(ctx context.Context, req *pb.SearchRecordRowsReq) (*pb.SearchRecordRowsRsp, error) {
	if strings.TrimSpace(req.GetViewId()) == "" {
		return &pb.SearchRecordRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("view_id is required"))}, nil
	}
	view, err := s.metadataReader.GetView(ctx, req.GetSpaceId(), req.GetViewId())
	if err != nil {
		return &pb.SearchRecordRowsRsp{RetInfo: response.Error(pb.ErrorCode_VIEW_NOT_FOUND, err)}, nil
	}
	if err := s.validateRecordView(ctx, view); err != nil {
		return &pb.SearchRecordRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	if view.GetActiveResult() == "" {
		return &pb.SearchRecordRowsRsp{RetInfo: response.Error(pb.ErrorCode_VIEW_NOT_FOUND, errors.New("view active_result is empty"))}, nil
	}
	datasetID := view.GetPrimaryDatasetId()
	if strings.TrimSpace(datasetID) == "" {
		return &pb.SearchRecordRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("view primary_dataset_id is required"))}, nil
	}
	viewColumns, _, err := s.metadataReader.ListViewColumns(ctx, req.GetSpaceId(), req.GetViewId(), &pb.Page{Size: 10000})
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
	rows, _, err := s.search.SearchRecordRows(ctx, searchsvc.SearchRequest{
		ResultName:   view.GetActiveResult(),
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
	viewMeta, err := s.metadataReader.GetView(ctx, req.GetSpaceId(), req.GetViewId())
	if err != nil {
		return &pb.RebuildTimeSeriesViewRsp{RetInfo: response.Error(pb.ErrorCode_VIEW_NOT_FOUND, err)}, nil
	}
	if err := s.validateTimeSeriesView(ctx, viewMeta); err != nil {
		return &pb.RebuildTimeSeriesViewRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	rebuildID := xid.New().String()
	rebuildReq := proto.Clone(req).(*pb.RebuildTimeSeriesViewReq)
	s.indexMu.Lock()
	if s.closing {
		s.indexMu.Unlock()
		return &pb.RebuildTimeSeriesViewRsp{RetInfo: response.Error(pb.ErrorCode_INNER_ERR, errors.New("service is closing"))}, nil
	}
	s.indexWG.Add(1)
	s.indexMu.Unlock()
	go func() {
		defer s.indexWG.Done()
		if err := s.rebuildTimeSeriesView(context.WithoutCancel(ctx), rebuildReq); err != nil {
			s.reportViewError(ctx, "time_series_view_rebuild", err)
		}
	}()
	return &pb.RebuildTimeSeriesViewRsp{RetInfo: response.Success("rebuild accepted"), RebuildId: rebuildID}, nil
}

func (s *Service) RebuildRecordView(ctx context.Context, req *pb.RebuildRecordViewReq) (*pb.RebuildRecordViewRsp, error) {
	if strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetViewId()) == "" {
		return &pb.RebuildRecordViewRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and view_id are required"))}, nil
	}
	viewMeta, err := s.metadataReader.GetView(ctx, req.GetSpaceId(), req.GetViewId())
	if err != nil {
		return &pb.RebuildRecordViewRsp{RetInfo: response.Error(pb.ErrorCode_VIEW_NOT_FOUND, err)}, nil
	}
	if err := s.validateRecordView(ctx, viewMeta); err != nil {
		return &pb.RebuildRecordViewRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	rebuildID := xid.New().String()
	rebuildReq := proto.Clone(req).(*pb.RebuildRecordViewReq)
	s.indexMu.Lock()
	if s.closing {
		s.indexMu.Unlock()
		return &pb.RebuildRecordViewRsp{RetInfo: response.Error(pb.ErrorCode_INNER_ERR, errors.New("service is closing"))}, nil
	}
	s.indexWG.Add(1)
	s.indexMu.Unlock()
	go func() {
		defer s.indexWG.Done()
		if err := s.rebuildRecordView(context.WithoutCancel(ctx), rebuildReq); err != nil {
			s.reportViewError(ctx, "record_view_rebuild", err)
		}
	}()
	return &pb.RebuildRecordViewRsp{RetInfo: response.Success("rebuild accepted"), RebuildId: rebuildID}, nil
}

func (s *Service) rebuildTimeSeriesView(ctx context.Context, req *pb.RebuildTimeSeriesViewReq) error {
	views, err := s.viewStore()
	if err != nil {
		return err
	}
	builder := view.NewBuilder(view.Options{
		Metadata: s.metadata,
		Facts:    s.timeSeriesFactReaderOrDefault(),
		Views:    views,
		OnBuildStarted: func(ctx context.Context, item *pb.View, targetVersion uint64, resultName string) {
			s.startViewDirtyTracking(pb.DataKind_DATA_KIND_TIME_SERIES, item, targetVersion, resultName)
		},
		BeforeComplete: func(ctx context.Context, item *pb.View, targetVersion uint64, resultName string) error {
			return s.drainTimeSeriesDirty(ctx, viewDirtyHandle(pb.DataKind_DATA_KIND_TIME_SERIES, item.GetSpaceId(), item.GetViewId(), targetVersion, resultName))
		},
		OnBuildFinished: func(ctx context.Context, item *pb.View, targetVersion uint64, resultName string) {
			s.stopViewDirtyTracking(viewDirtyHandle(pb.DataKind_DATA_KIND_TIME_SERIES, item.GetSpaceId(), item.GetViewId(), targetVersion, resultName))
		},
	})
	_, err = builder.BuildView(ctx, req.GetSpaceId(), req.GetViewId())
	return err
}

func (s *Service) rebuildRecordView(ctx context.Context, req *pb.RebuildRecordViewReq) error {
	view, err := s.metadataReader.GetView(ctx, req.GetSpaceId(), req.GetViewId())
	if err != nil {
		return err
	}
	if err := s.validateRecordView(ctx, view); err != nil {
		return err
	}
	if strings.TrimSpace(view.GetPrimaryDatasetId()) == "" {
		return errors.New("view primary_dataset_id is required")
	}
	columns, _, err := s.metadataReader.ListViewColumns(ctx, req.GetSpaceId(), req.GetViewId(), &pb.Page{Size: 10000})
	if err != nil {
		return err
	}
	if !isProjectableRecordView(view, columns) {
		return fmt.Errorf("record view %s/%s contains unsupported columns for bleve projection", req.GetSpaceId(), req.GetViewId())
	}
	targetVersion := view.GetViewVersion()
	if targetVersion == 0 {
		targetVersion = 1
	}
	resultName := searchsvc.RecordIndexName(req.GetSpaceId(), req.GetViewId(), targetVersion, time.Now().UTC())
	if _, err := s.metadata.BeginViewBuild(ctx, req.GetSpaceId(), req.GetViewId(), targetVersion, resultName); err != nil {
		return err
	}
	if err := s.search.IndexRecordViewRows(ctx, resultName, columns, nil); err != nil {
		_ = s.metadata.FailViewBuild(ctx, req.GetSpaceId(), req.GetViewId(), targetVersion, resultName, err)
		return err
	}
	dirtyHandle := s.startViewDirtyTracking(pb.DataKind_DATA_KIND_RECORD, view, targetVersion, resultName)
	defer s.stopViewDirtyTracking(dirtyHandle)
	failBuild := func(buildErr error) error {
		_ = s.metadata.FailViewBuild(ctx, req.GetSpaceId(), req.GetViewId(), targetVersion, resultName, buildErr)
		return buildErr
	}
	cursor := ""
	reader := s.viewFactReaderOrDefault()
	for {
		rows, page, err := reader.ScanRecordRows(ctx, req.GetSpaceId(), view.GetPrimaryDatasetId(), nil, nil, &pb.Page{Size: rebuildViewPageSize, Cursor: cursor})
		if err != nil {
			return failBuild(err)
		}
		if len(rows) > 0 {
			projected, ok, err := s.recordRowsForView(ctx, view, columns, rows)
			if err != nil {
				return failBuild(err)
			}
			if !ok {
				return failBuild(fmt.Errorf("record view %s/%s contains unsupported columns for bleve projection", req.GetSpaceId(), req.GetViewId()))
			}
			if err := s.search.IndexRecordViewRows(ctx, resultName, columns, projected); err != nil {
				return failBuild(err)
			}
		}
		if !page.GetHasMore() || page.GetNextCursor() == "" {
			break
		}
		cursor = page.GetNextCursor()
	}
	if err := s.drainRecordDirty(ctx, dirtyHandle, columns); err != nil {
		return failBuild(err)
	}
	return s.metadata.CompleteViewBuild(ctx, req.GetSpaceId(), req.GetViewId(), targetVersion, resultName)
}

func (s *Service) validateTimeSeriesView(ctx context.Context, view *pb.View) error {
	if view == nil {
		return errors.New("view is required")
	}
	if !strings.EqualFold(strings.TrimSpace(view.GetEngine()), "duckdb") {
		return fmt.Errorf("time series view %s requires duckdb engine", view.GetViewId())
	}
	dataset, err := s.metadataReader.GetDataset(ctx, view.GetSpaceId(), view.GetPrimaryDatasetId())
	if err != nil {
		return err
	}
	if dataset.GetDataKind() != pb.DataKind_DATA_KIND_TIME_SERIES {
		return fmt.Errorf("time series view %s requires time series primary dataset", view.GetViewId())
	}
	return nil
}

func (s *Service) validateRecordView(ctx context.Context, view *pb.View) error {
	if view == nil {
		return errors.New("view is required")
	}
	if !strings.EqualFold(strings.TrimSpace(view.GetEngine()), "bleve") {
		return fmt.Errorf("record view %s requires bleve engine", view.GetViewId())
	}
	dataset, err := s.metadataReader.GetDataset(ctx, view.GetSpaceId(), view.GetPrimaryDatasetId())
	if err != nil {
		return err
	}
	if dataset.GetDataKind() != pb.DataKind_DATA_KIND_RECORD {
		return fmt.Errorf("record view %s requires record primary dataset", view.GetViewId())
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
		out = append(out, &pb.ResultColumn{
			ColumnName: column.GetColumnName(),
			OriginType: column.GetOriginType(),
			DatasetId:  viewColumnDatasetID(column),
			OriginId:   column.GetOriginId(),
			ValueType:  column.GetValueType(),
		})
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
		sortRecordRows(rows)
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

func errText(msg string) error {
	return stringError(msg)
}

type stringError string

func (e stringError) Error() string { return string(e) }
