package storage

import (
	"context"
	"sort"
	"strings"

	devicebleve "github.com/mooyang-code/moox/modules/storage/internal/services/device/bleve"
	"github.com/mooyang-code/moox/modules/storage/pkg/quantstore"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
)

func (s *Service) QueryView(ctx context.Context, req *pb.QueryViewReq) (*pb.QueryViewRsp, error) {
	if req.GetViewId() == "" {
		return &pb.QueryViewRsp{RetInfo: quantstore.Error(pb.ErrorCode_VIEW_NOT_FOUND, errText("view_id is required"))}, nil
	}
	view, err := s.metadata.GetView(ctx, req.GetSpaceId(), req.GetViewId())
	if err != nil {
		return &pb.QueryViewRsp{RetInfo: quantstore.Error(pb.ErrorCode_VIEW_NOT_FOUND, err)}, nil
	}
	if view.GetActiveResult() == "" {
		return &pb.QueryViewRsp{RetInfo: quantstore.Error(pb.ErrorCode_VIEW_NOT_FOUND, errText("view active_result is empty"))}, nil
	}
	viewStore, err := s.viewStore()
	if err != nil {
		return &pb.QueryViewRsp{RetInfo: quantstore.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	columns, rows, page, err := viewStore.QueryView(ctx, view.GetActiveResult(), req)
	if err != nil {
		return &pb.QueryViewRsp{RetInfo: quantstore.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	return &pb.QueryViewRsp{RetInfo: quantstore.Success("success"), Columns: columns, Rows: rows, PageResult: page}, nil
}

func (s *Service) SearchRows(ctx context.Context, req *pb.SearchRowsReq) (*pb.SearchRowsRsp, error) {
	if req.GetDatasetId() == "" {
		return &pb.SearchRowsRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errText("dataset_id is required"))}, nil
	}
	if strings.TrimSpace(req.GetTextQuery()) != "" {
		index, err := s.searchIndex()
		if err != nil {
			return &pb.SearchRowsRsp{RetInfo: quantstore.Error(pb.ErrorCode_INNER_ERR, err)}, nil
		}
		rows, _, err := index.SearchRows(ctx, devicebleve.SearchRequest{
			SpaceID:    req.GetSpaceId(),
			DatasetID:  req.GetDatasetId(),
			SubjectIDs: req.GetSubjectIds(),
			TextQuery:  req.GetTextQuery(),
			TimeRange:  req.GetTimeRange(),
		})
		if err != nil {
			return &pb.SearchRowsRsp{RetInfo: quantstore.Error(pb.ErrorCode_INNER_ERR, err)}, nil
		}
		var matched []*pb.DataRow
		for _, row := range rows {
			if rowMatchesFilters(row, req.GetFilters()) {
				matched = append(matched, projectRowColumns(row, req.GetColumnNames()))
			}
		}
		sortSearchRows(matched, req.GetSorts())
		paged, page := pageSlice(matched, req.GetPage())
		return &pb.SearchRowsRsp{RetInfo: quantstore.Success("success"), Rows: paged, PageResult: page}, nil
	}
	subjectIDs := req.GetSubjectIds()
	if len(subjectIDs) == 0 {
		subjectIDs = []string{""}
	}

	var matched []*pb.DataRow
	for _, subjectID := range subjectIDs {
		rows, _, err := s.store.ReadRows(
			ctx,
			&pb.DataScope{SpaceId: req.GetSpaceId(), DatasetId: req.GetDatasetId(), SubjectId: subjectID},
			pb.ReadMode_READ_MODE_RANGE,
			req.GetTimeRange(),
			"",
			nil,
			nil,
			req.GetPage(),
		)
		if err != nil {
			return &pb.SearchRowsRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
		}
		for _, row := range rows {
			if textRowMatch(row, req.GetTextQuery()) && rowMatchesFilters(row, req.GetFilters()) {
				matched = append(matched, projectRowColumns(row, req.GetColumnNames()))
			}
		}
	}
	sortSearchRows(matched, req.GetSorts())
	paged, page := pageSlice(matched, req.GetPage())
	return &pb.SearchRowsRsp{RetInfo: quantstore.Success("success"), Rows: paged, PageResult: page}, nil
}

func textRowMatch(row *pb.DataRow, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return true
	}
	for _, column := range row.GetColumns() {
		if strings.Contains(strings.ToLower(column.GetColumnName()), query) || strings.Contains(strings.ToLower(typedValueString(column.GetValue())), query) {
			return true
		}
	}
	for _, value := range row.GetAttributes() {
		if strings.Contains(strings.ToLower(value), query) {
			return true
		}
	}
	return false
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
	leftText := typedValueString(left)
	rightText := typedValueString(right)
	switch op {
	case "==":
		return leftText == rightText
	case "!=":
		return leftText != rightText
	case ">":
		return leftText > rightText
	case ">=":
		return leftText >= rightText
	case "<":
		return leftText < rightText
	case "<=":
		return leftText <= rightText
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
	if value == nil {
		return ""
	}
	switch v := value.GetValue().(type) {
	case *pb.TypedValue_StringValue:
		return v.StringValue
	case *pb.TypedValue_TimeValue:
		return v.TimeValue
	case *pb.TypedValue_JsonValue:
		return v.JsonValue
	case *pb.TypedValue_IntValue:
		return fmtInt(v.IntValue)
	case *pb.TypedValue_DoubleValue:
		return fmtDouble(v.DoubleValue)
	case *pb.TypedValue_BoolValue:
		if v.BoolValue {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

func errText(msg string) error {
	return stringError(msg)
}

type stringError string

func (e stringError) Error() string { return string(e) }
