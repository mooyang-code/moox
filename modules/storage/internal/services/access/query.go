package access

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/core/factvalue"
	"github.com/mooyang-code/moox/modules/storage/internal/core/response"
	searchsvc "github.com/mooyang-code/moox/modules/storage/internal/services/search"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/rs/xid"
	"google.golang.org/protobuf/proto"
)

const rebuildSearchIndexPageSize = 1000

func (s *Service) QueryView(ctx context.Context, req *pb.QueryViewReq) (*pb.QueryViewRsp, error) {
	if req.GetViewId() == "" {
		return &pb.QueryViewRsp{RetInfo: response.Error(pb.ErrorCode_VIEW_NOT_FOUND, errText("view_id is required"))}, nil
	}
	view, err := s.metadataReader.GetView(ctx, req.GetSpaceId(), req.GetViewId())
	if err != nil {
		return &pb.QueryViewRsp{RetInfo: response.Error(pb.ErrorCode_VIEW_NOT_FOUND, err)}, nil
	}
	if view.GetActiveResult() == "" {
		return &pb.QueryViewRsp{RetInfo: response.Error(pb.ErrorCode_VIEW_NOT_FOUND, errText("view active_result is empty"))}, nil
	}
	viewStore, err := s.viewStore()
	if err != nil {
		return &pb.QueryViewRsp{RetInfo: response.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	columns, rows, page, err := viewStore.QueryView(ctx, view.GetActiveResult(), req)
	if err != nil {
		return &pb.QueryViewRsp{RetInfo: response.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	return &pb.QueryViewRsp{RetInfo: response.Success("success"), Columns: columns, Rows: rows, PageResult: page}, nil
}

func (s *Service) SearchRows(ctx context.Context, req *pb.SearchRowsReq) (*pb.SearchRowsRsp, error) {
	if req.GetDatasetId() == "" {
		return &pb.SearchRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errText("dataset_id is required"))}, nil
	}
	rows, _, err := s.search.SearchRows(ctx, searchsvc.SearchRequest{
		SpaceID:    req.GetSpaceId(),
		DatasetID:  req.GetDatasetId(),
		SubjectIDs: req.GetSubjectIds(),
		TextQuery:  req.GetTextQuery(),
		TimeRange:  req.GetTimeRange(),
	})
	if err != nil {
		return &pb.SearchRowsRsp{RetInfo: response.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	var matched []*pb.DataRow
	for _, row := range rows {
		if rowMatchesFilters(row, req.GetFilters()) {
			matched = append(matched, projectRowColumns(row, req.GetColumnNames()))
		}
	}
	sortSearchRows(matched, req.GetSorts())
	paged, page := pageSlice(matched, req.GetPage())
	return &pb.SearchRowsRsp{RetInfo: response.Success("success"), Rows: paged, PageResult: page}, nil
}

func (s *Service) RebuildSearchIndex(ctx context.Context, req *pb.RebuildSearchIndexReq) (*pb.RebuildSearchIndexRsp, error) {
	if strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetDatasetId()) == "" {
		return &pb.RebuildSearchIndexRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and dataset_id are required"))}, nil
	}
	subjectIDs, err := s.rebuildSearchIndexSubjects(ctx, req)
	if err != nil {
		return &pb.RebuildSearchIndexRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	if len(subjectIDs) == 0 {
		return &pb.RebuildSearchIndexRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("subject_ids are required when dataset has no subject bindings"))}, nil
	}
	if err := s.requireDataSetSubjectsBound(ctx, req.GetSpaceId(), req.GetDatasetId(), subjectIDs); err != nil {
		return &pb.RebuildSearchIndexRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	rebuildID := xid.New().String()
	rebuildReq := proto.Clone(req).(*pb.RebuildSearchIndexReq)
	s.indexMu.Lock()
	if s.closing {
		s.indexMu.Unlock()
		return &pb.RebuildSearchIndexRsp{RetInfo: response.Error(pb.ErrorCode_INNER_ERR, errors.New("service is closing"))}, nil
	}
	s.indexWG.Add(1)
	s.indexMu.Unlock()
	go func() {
		defer s.indexWG.Done()
		if _, err := s.rebuildSearchIndex(context.WithoutCancel(ctx), rebuildReq, subjectIDs); err != nil {
			s.reportDerivedError(ctx, "search_rebuild_index", err)
		}
	}()
	return &pb.RebuildSearchIndexRsp{RetInfo: response.Success("rebuild accepted"), RebuildId: rebuildID}, nil
}

func (s *Service) rebuildSearchIndex(ctx context.Context, req *pb.RebuildSearchIndexReq, subjectIDs []string) (uint64, error) {
	var indexed uint64
	for _, subjectID := range subjectIDs {
		cursor := ""
		for {
			readRsp, err := s.ReadRows(ctx, &pb.ReadRowsReq{
				AuthInfo: req.GetAuthInfo(),
				Scope: &pb.DataScope{
					SpaceId:    req.GetSpaceId(),
					DatasetId:  req.GetDatasetId(),
					SubjectId:  subjectID,
					Freq:       req.GetFreq(),
					Dimensions: req.GetDimensions(),
				},
				ReadMode:  pb.ReadMode_READ_MODE_RANGE,
				TimeRange: req.GetTimeRange(),
				Page:      &pb.Page{Size: rebuildSearchIndexPageSize, Cursor: cursor},
			})
			if err != nil {
				return indexed, err
			}
			if readRsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
				return indexed, errText(readRsp.GetRetInfo().GetMsg())
			}
			if len(readRsp.GetRows()) > 0 {
				if err := s.search.IndexRows(ctx, readRsp.GetRows()); err != nil {
					return indexed, err
				}
				indexed += uint64(len(readRsp.GetRows()))
			}
			page := readRsp.GetPageResult()
			if !page.GetHasMore() || page.GetNextCursor() == "" {
				break
			}
			cursor = page.GetNextCursor()
		}
	}
	return indexed, nil
}

func (s *Service) rebuildSearchIndexSubjects(ctx context.Context, req *pb.RebuildSearchIndexReq) ([]string, error) {
	if len(req.GetSubjectIds()) > 0 {
		return uniqueNonEmpty(req.GetSubjectIds()), nil
	}
	const pageSize = 1000
	var subjects []string
	for pageNo := uint32(1); ; pageNo++ {
		bindings, page, err := s.metadata.ListDataSetSubjectsPage(ctx, req.GetSpaceId(), req.GetDatasetId(), "", &pb.Page{Page: pageNo, Size: pageSize})
		if err != nil {
			return nil, err
		}
		for _, binding := range bindings {
			if subjectID := strings.TrimSpace(binding.GetSubjectId()); subjectID != "" {
				subjects = append(subjects, subjectID)
			}
		}
		if !page.GetHasMore() {
			break
		}
	}
	return uniqueNonEmpty(subjects), nil
}

func (s *Service) requireDataSetSubjectsBound(ctx context.Context, spaceID string, datasetID string, subjectIDs []string) error {
	for _, subjectID := range subjectIDs {
		bindings, _, err := s.metadata.ListDataSetSubjectsPage(ctx, spaceID, datasetID, subjectID, &pb.Page{Page: 1, Size: 1})
		if err != nil {
			return err
		}
		if len(bindings) == 0 {
			return errors.New("subject " + subjectID + " is not bound to dataset " + datasetID)
		}
	}
	return nil
}

func uniqueNonEmpty(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func rowMatchesFilters(row *pb.DataRow, filters []*pb.FilterExpr) bool {
	for _, filter := range filters {
		if !rowMatchesFilter(row, filter) {
			return false
		}
	}
	return true
}

func rowMatchesFilter(row *pb.DataRow, filter *pb.FilterExpr) bool {
	if filter == nil || strings.TrimSpace(filter.GetExpr()) == "" {
		return true
	}
	left, op, right, ok := parseSimpleFilter(filter.GetExpr())
	if !ok {
		return false
	}
	rowValue, ok := rowColumnValue(row, left)
	if !ok {
		return false
	}
	argName := strings.TrimPrefix(right, "$")
	expected := filter.GetArgs()[argName]
	if expected == nil {
		return false
	}
	return compareTypedValues(rowValue, expected, op)
}

func parseSimpleFilter(expr string) (left, op, right string, ok bool) {
	for _, candidate := range []string{"==", "!=", ">=", "<=", ">", "<"} {
		if parts := strings.Split(expr, candidate); len(parts) == 2 {
			left = strings.TrimSpace(parts[0])
			right = strings.TrimSpace(parts[1])
			if left == "" || !strings.HasPrefix(right, "$") {
				return "", "", "", false
			}
			return left, candidate, right, true
		}
	}
	return "", "", "", false
}

func rowColumnValue(row *pb.DataRow, name string) (*pb.TypedValue, bool) {
	for _, column := range row.GetColumns() {
		if column.GetColumnName() == name {
			return column.GetValue(), true
		}
	}
	return nil, false
}

func compareTypedValues(left, right *pb.TypedValue, op string) bool {
	cmp := factvalue.Compare(left, right)
	switch op {
	case "==":
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

func projectRowColumns(row *pb.DataRow, includes []string) *pb.DataRow {
	if len(includes) == 0 {
		return row
	}
	allow := make(map[string]bool, len(includes))
	for _, name := range includes {
		allow[name] = true
	}
	filtered := proto.Clone(row).(*pb.DataRow)
	filtered.Columns = filtered.Columns[:0]
	for _, column := range row.GetColumns() {
		if allow[column.GetColumnName()] {
			filtered.Columns = append(filtered.Columns, column)
		}
	}
	return filtered
}

func sortSearchRows(rows []*pb.DataRow, sorts []*pb.SortSpec) {
	if len(sorts) == 0 {
		return
	}
	sort.SliceStable(rows, func(i, j int) bool {
		for _, spec := range sorts {
			left, _ := rowColumnValue(rows[i], spec.GetFieldName())
			right, _ := rowColumnValue(rows[j], spec.GetFieldName())
			leftText := typedValueString(left)
			rightText := typedValueString(right)
			if leftText == rightText {
				continue
			}
			if spec.GetDesc() {
				return leftText > rightText
			}
			return leftText < rightText
		}
		if rows[i].GetKey().GetScope().GetSubjectId() == rows[j].GetKey().GetScope().GetSubjectId() {
			return rows[i].GetKey().GetDataTime() < rows[j].GetKey().GetDataTime()
		}
		return rows[i].GetKey().GetScope().GetSubjectId() < rows[j].GetKey().GetScope().GetSubjectId()
	})
}

func typedValueString(value *pb.TypedValue) string {
	return factvalue.String(value)
}

func errText(msg string) error {
	return stringError(msg)
}

type stringError string

func (e stringError) Error() string { return string(e) }
