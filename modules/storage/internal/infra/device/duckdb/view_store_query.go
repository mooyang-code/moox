//go:build cgo

package duckdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	_ "github.com/marcboeker/go-duckdb/v2"
	"github.com/mooyang-code/moox/modules/storage/internal/core/factkey"
	"github.com/mooyang-code/moox/modules/storage/internal/core/factvalue"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// Options 保存 DuckDB 视图存储打开配置。

func rowKeyPredicate(rows []*pb.TimeSeriesRow) (string, []any) {
	var b strings.Builder
	args := make([]any, 0, len(rows)*2)
	for _, row := range rows {
		if row == nil {
			continue
		}
		if b.Len() > 0 {
			b.WriteString(" OR ")
		}
		b.WriteString("(row_key = ? AND data_time = ?)")
		args = append(args, timeSeriesRowKey(row), normalizeRowDataTime(row))
	}
	return b.String(), args
}

func (s *ViewStore) ListResultTables(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT table_name
		FROM information_schema.tables
		WHERE table_name LIKE 'view_%'
		ORDER BY table_name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			return nil, err
		}
		out = append(out, tableName)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *ViewStore) DropResultTable(ctx context.Context, tableName string) error {
	quoted, err := quoteTableName(tableName)
	if err != nil {
		return err
	}
	unlock := s.lockResultTable(tableName)
	defer unlock()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS %s`, quoted)); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM moox_view_columns WHERE table_name = ?`, tableName); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM moox_view_index_meta WHERE table_name = ?`, tableName); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.indexedTables.Delete(tableName)
	return nil
}

func (s *ViewStore) QueryTimeSeriesRows(ctx context.Context, tableName string, req *pb.QueryTimeSeriesRowsReq) ([]*pb.ResultColumn, []*pb.TimeSeriesRow, *pb.PageResult, error) {
	quoted, err := quoteTableName(tableName)
	if err != nil {
		return nil, nil, nil, err
	}
	columns, err := s.loadColumns(ctx, tableName)
	if err != nil {
		return nil, nil, nil, err
	}
	plan, err := buildTimeSeriesQuery(quoted, columns, req)
	if err != nil {
		return nil, nil, nil, err
	}
	projectedColumns := projectColumns(columns, req.GetColumnNames())
	var total uint64
	if plan.countSQL != "" {
		if err := s.db.QueryRowContext(ctx, plan.countSQL, plan.args...).Scan(&total); err != nil {
			return nil, nil, nil, err
		}
	}
	if plan.keySQL != "" {
		keys, err := s.queryTimeSeriesPageKeys(ctx, plan.keySQL, plan.args)
		if err != nil {
			return nil, nil, nil, err
		}
		hasMore := uint64(plan.pageNo)*uint64(plan.size) < total
		if plan.countSQL == "" {
			total = 0
			if uint32(len(keys)) > plan.size {
				hasMore = true
				keys = keys[:plan.size]
			} else {
				hasMore = false
			}
		}
		out, err := s.fetchTimeSeriesRowsByResultKeys(ctx, quoted, plan.selectColumns, projectedColumns, keys)
		if err != nil {
			return nil, nil, nil, err
		}
		return projectedColumns, out, &pb.PageResult{
			Page:       plan.pageNo,
			Size:       plan.size,
			Total:      uint32(total),
			HasMore:    hasMore,
			TotalState: plan.totalState,
		}, nil
	}
	rows, err := s.db.QueryContext(ctx, plan.sqlText, plan.args...)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()
	out, err := scanResultRows(rows, projectedColumns)
	if err != nil {
		return nil, nil, nil, err
	}
	hasMore := uint64(plan.pageNo*plan.size) < total
	if plan.countSQL == "" {
		total = 0
		if uint32(len(out)) > plan.size {
			hasMore = true
			out = out[:plan.size]
		} else {
			hasMore = false
		}
	}
	return projectedColumns, out, &pb.PageResult{
		Page:       plan.pageNo,
		Size:       plan.size,
		Total:      uint32(total),
		HasMore:    hasMore,
		TotalState: plan.totalState,
	}, nil
}

func (s *ViewStore) queryTimeSeriesPageKeys(ctx context.Context, sqlText string, args []any) ([]resultRowKey, error) {
	rows, err := s.db.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []resultRowKey
	for rows.Next() {
		var key resultRowKey
		if err := rows.Scan(&key.rowKey, &key.dataTime); err != nil {
			return nil, err
		}
		out = append(out, key)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func resultRowKeyPredicate(keys []resultRowKey) (string, []any) {
	var b strings.Builder
	args := make([]any, 0, len(keys)*2)
	for _, key := range keys {
		if key.rowKey == "" || key.dataTime == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString(" OR ")
		}
		b.WriteString("(row_key = ? AND data_time = ?)")
		args = append(args, key.rowKey, key.dataTime)
	}
	return b.String(), args
}

func (s *ViewStore) tableExists(ctx context.Context, tableName string) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM information_schema.tables
		WHERE table_name = ?
	`, tableName).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

type timeSeriesQueryPlan struct {
	sqlText       string
	keySQL        string
	countSQL      string
	selectColumns []string
	args          []any
	pageNo        uint32
	size          uint32
	preview       bool
	totalState    pb.TotalState
}

func buildTimeSeriesQuery(quotedTableName string, columns []*pb.ResultColumn, req *pb.QueryTimeSeriesRowsReq) (*timeSeriesQueryPlan, error) {
	where, args, err := buildSQLPredicates(req, columns)
	if err != nil {
		return nil, err
	}
	selectColumns, err := resultSelectColumnsForRequest(columns, req.GetColumnNames())
	if err != nil {
		return nil, err
	}
	orderBy, err := buildOrderBy(req.GetSorts(), columns)
	if err != nil {
		return nil, err
	}
	pageNo, size, preview := queryWindow(req)
	baseSQL := fmt.Sprintf(`SELECT %s FROM %s`, strings.Join(selectColumns, ","), quotedTableName)
	countBaseSQL := fmt.Sprintf(`SELECT 1 FROM %s`, quotedTableName)
	countSQL := fmt.Sprintf(`SELECT COUNT(*) FROM %s`, quotedTableName)
	if where != "" {
		baseSQL += " WHERE " + where
		countBaseSQL += " WHERE " + where
		countSQL += " WHERE " + where
	}
	if orderBy != "" {
		baseSQL += " ORDER BY " + orderBy
	}
	totalState := pb.TotalState_EXACT
	if !shouldCountTimeSeries(req, preview, where != "") {
		countSQL = ""
		totalState = pb.TotalState_SKIPPED
	}
	limit := size
	if countSQL == "" {
		limit = size + 1
	}
	offset := uint64(0)
	if !preview {
		offset = uint64(pageNo-1) * uint64(size)
	}
	sqlText := baseSQL
	keySQL := ""
	if orderBy != "" {
		keySQL = fmt.Sprintf(`SELECT "row_key","data_time" FROM %s`, quotedTableName)
		if where != "" {
			keySQL += " WHERE " + where
		}
		keySQL += " ORDER BY " + orderBy
		if req.GetLimit() > 0 && timeSeriesRequestHasPaging(req.GetPage()) {
			innerLimit := pagedInnerLimit(req.GetLimit(), offset+uint64(limit))
			keySQL = fmt.Sprintf("SELECT * FROM (%s LIMIT %d) AS moox_limited_keys", keySQL, innerLimit)
			if countSQL != "" {
				countSQL = fmt.Sprintf("SELECT COUNT(*) FROM (%s LIMIT %d) AS moox_limited_count", countBaseSQL, req.GetLimit())
			}
		}
		keySQL += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, offset)
	} else if req.GetLimit() > 0 && timeSeriesRequestHasPaging(req.GetPage()) {
		innerLimit := pagedInnerLimit(req.GetLimit(), offset+uint64(limit))
		sqlText = fmt.Sprintf("SELECT * FROM (%s LIMIT %d) AS moox_limited", baseSQL, innerLimit)
		if countSQL != "" {
			countSQL = fmt.Sprintf("SELECT COUNT(*) FROM (%s LIMIT %d) AS moox_limited_count", countBaseSQL, req.GetLimit())
		}
	}
	if keySQL == "" {
		sqlText += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, offset)
	}
	return &timeSeriesQueryPlan{
		sqlText:       sqlText,
		keySQL:        keySQL,
		countSQL:      countSQL,
		selectColumns: selectColumns,
		args:          args,
		pageNo:        pageNo,
		size:          size,
		preview:       preview,
		totalState:    totalState,
	}, nil
}

func pagedInnerLimit(requestLimit uint32, neededRows uint64) uint32 {
	if requestLimit == 0 {
		return 0
	}
	if neededRows == 0 || neededRows > uint64(requestLimit) {
		return requestLimit
	}
	return uint32(neededRows)
}

func queryWindow(req *pb.QueryTimeSeriesRowsReq) (uint32, uint32, bool) {
	if req.GetLimit() > 0 && !timeSeriesRequestHasPaging(req.GetPage()) {
		return 1, req.GetLimit(), true
	}
	pageNo, size := normalizePage(req.GetPage())
	return pageNo, size, false
}

func shouldCountTimeSeries(req *pb.QueryTimeSeriesRowsReq, preview bool, hasPredicate bool) bool {
	switch req.GetTotalMode() {
	case pb.TotalMode_NONE:
		return false
	case pb.TotalMode_FORCE_EXACT:
		return true
	default:
		if preview {
			return false
		}
		return hasPredicate
	}
}

func timeSeriesRequestHasPaging(page *pb.Page) bool {
	if page == nil {
		return false
	}
	return page.GetPage() > 0 || page.GetSize() > 0 || page.GetCursor() != ""
}

func hasEffectiveTimeSeriesKey(keys []*pb.TimeSeriesKey) bool {
	for _, key := range keys {
		if key == nil {
			continue
		}
		if strings.TrimSpace(key.GetSpaceId()) != "" ||
			strings.TrimSpace(key.GetDatasetId()) != "" ||
			strings.TrimSpace(key.GetSubjectId()) != "" ||
			strings.TrimSpace(key.GetFreq()) != "" ||
			strings.TrimSpace(key.GetDataTime()) != "" ||
			len(key.GetDimensions()) > 0 {
			return true
		}
	}
	return false
}

func buildSQLPredicates(req *pb.QueryTimeSeriesRowsReq, columns []*pb.ResultColumn) (string, []any, error) {
	var clauses []string
	var args []any
	if keyClause, keyArgs, err := buildKeyPredicates(req.GetKeys()); err != nil {
		return "", nil, err
	} else if keyClause != "" {
		clauses = append(clauses, keyClause)
		args = append(args, keyArgs...)
	}
	if timeRange := req.GetTimeRange(); timeRange != nil {
		if start := strings.TrimSpace(timeRange.GetStartTime()); start != "" {
			normalized, err := factkey.NormalizeTimeVersion(start)
			if err != nil {
				return "", nil, errors.New("start_time must be RFC3339/RFC3339Nano")
			}
			clauses = append(clauses, "data_time >= ?")
			args = append(args, normalized)
		}
		if end := strings.TrimSpace(timeRange.GetEndTime()); end != "" {
			normalized, err := factkey.NormalizeTimeVersion(end)
			if err != nil {
				return "", nil, errors.New("end_time must be RFC3339/RFC3339Nano")
			}
			clauses = append(clauses, "data_time <= ?")
			args = append(args, normalized)
		}
	}
	filterClauses, filterArgs, err := buildFilterPredicates(req.GetFilters(), columns)
	if err != nil {
		return "", nil, err
	}
	clauses = append(clauses, filterClauses...)
	args = append(args, filterArgs...)
	return strings.Join(clauses, " AND "), args, nil
}

func buildKeyPredicates(keys []*pb.TimeSeriesKey) (string, []any, error) {
	var clauses []string
	var args []any
	for _, key := range keys {
		if key == nil {
			continue
		}
		var parts []string
		if rowKeyClause, rowKeyArgs, ok, err := buildRowKeyPredicateForKey(key); err != nil {
			return "", nil, err
		} else if ok {
			if spaceID := strings.TrimSpace(key.GetSpaceId()); spaceID != "" {
				parts = append(parts, `"space_id" = ?`)
				args = append(args, spaceID)
			}
			parts = append(parts, rowKeyClause)
			args = append(args, rowKeyArgs...)
			if dataTime := strings.TrimSpace(key.GetDataTime()); dataTime != "" {
				normalized, err := factkey.NormalizeTimeVersion(dataTime)
				if err != nil {
					return "", nil, errors.New("data_time must be RFC3339/RFC3339Nano")
				}
				parts = append(parts, "data_time = ?")
				args = append(args, normalized)
			}
			clauses = append(clauses, "("+strings.Join(parts, " AND ")+")")
			continue
		}
		addString := func(column string, value string) error {
			if strings.TrimSpace(value) == "" {
				return nil
			}
			quoted, err := quoteColumnName(column)
			if err != nil {
				return err
			}
			parts = append(parts, quoted+" = ?")
			args = append(args, value)
			return nil
		}
		if err := addString("space_id", key.GetSpaceId()); err != nil {
			return "", nil, err
		}
		if err := addString("dataset_id", key.GetDatasetId()); err != nil {
			return "", nil, err
		}
		if err := addString("subject_id", key.GetSubjectId()); err != nil {
			return "", nil, err
		}
		if err := addString("freq", key.GetFreq()); err != nil {
			return "", nil, err
		}
		if len(key.GetDimensions()) > 0 {
			raw, err := json.Marshal(key.GetDimensions())
			if err != nil {
				return "", nil, err
			}
			parts = append(parts, "dimensions_json = ?")
			args = append(args, string(raw))
		}
		if dataTime := strings.TrimSpace(key.GetDataTime()); dataTime != "" {
			normalized, err := factkey.NormalizeTimeVersion(dataTime)
			if err != nil {
				return "", nil, errors.New("data_time must be RFC3339/RFC3339Nano")
			}
			parts = append(parts, "data_time = ?")
			args = append(args, normalized)
		}
		if len(parts) > 0 {
			clauses = append(clauses, "("+strings.Join(parts, " AND ")+")")
		}
	}
	if len(clauses) == 0 {
		return "", args, nil
	}
	return "(" + strings.Join(clauses, " OR ") + ")", args, nil
}

func buildRowKeyPredicateForKey(key *pb.TimeSeriesKey) (string, []any, bool, error) {
	datasetID := strings.TrimSpace(key.GetDatasetId())
	subjectID := strings.TrimSpace(key.GetSubjectId())
	freq := strings.TrimSpace(key.GetFreq())
	if datasetID == "" || subjectID == "" || freq == "" {
		return "", nil, false, nil
	}
	if len(key.GetDimensions()) > 0 {
		raw, err := json.Marshal(key.GetDimensions())
		if err != nil {
			return "", nil, false, err
		}
		return `(row_key = ? AND dimensions_json = ?)`, []any{
			timeSeriesKeyRowKey(datasetID, subjectID, freq, key.GetDimensions()),
			string(raw),
		}, true, nil
	}
	return `row_key LIKE ? ESCAPE '\'`, []any{escapeSQLLike(timeSeriesKeyRowKeyPrefix(datasetID, subjectID, freq)) + "%"}, true, nil
}

func buildFilterPredicates(filters []*pb.FilterExpr, columns []*pb.ResultColumn) ([]string, []any, error) {
	if len(filters) == 0 {
		return nil, nil, nil
	}
	columnTypes := resultColumnTypes(columns)
	var clauses []string
	var args []any
	for _, filter := range filters {
		if filter == nil || strings.TrimSpace(filter.GetExpr()) == "" {
			continue
		}
		if fn, field, token, ok := parseFunctionFilter(filter.GetExpr()); ok {
			if _, ok := columnTypes[field]; !ok {
				return nil, nil, fmt.Errorf("unsupported filter field %q", field)
			}
			quoted, err := quoteColumnName(field)
			if err != nil {
				return nil, nil, err
			}
			switch fn {
			case "is_empty":
				clauses = append(clauses, fmt.Sprintf("(%s IS NULL OR CAST(%s AS VARCHAR) = '')", quoted, quoted))
				continue
			case "is_not_empty":
				clauses = append(clauses, fmt.Sprintf("(%s IS NOT NULL AND CAST(%s AS VARCHAR) <> '')", quoted, quoted))
				continue
			}
			value := filterValue(token, filter.GetArgs())
			if value == nil {
				return nil, nil, fmt.Errorf("unsupported filter value %q", token)
			}
			textValue := factvalue.String(value)
			switch fn {
			case "starts_with":
				clauses = append(clauses, fmt.Sprintf("CAST(%s AS VARCHAR) LIKE ?", quoted))
				args = append(args, textValue+"%")
				continue
			case "ends_with":
				clauses = append(clauses, fmt.Sprintf("CAST(%s AS VARCHAR) LIKE ?", quoted))
				args = append(args, "%"+textValue)
				continue
			case "not_contains":
				clauses = append(clauses, fmt.Sprintf("(%s IS NULL OR CAST(%s AS VARCHAR) NOT LIKE ?)", quoted, quoted))
				args = append(args, "%"+textValue+"%")
				continue
			default:
				return nil, nil, fmt.Errorf("unsupported filter expression %q", filter.GetExpr())
			}
		}
		left, op, right, ok := parseSimpleFilter(filter.GetExpr())
		if !ok {
			return nil, nil, fmt.Errorf("unsupported filter expression %q", filter.GetExpr())
		}
		valueType, ok := columnTypes[left]
		if !ok {
			return nil, nil, fmt.Errorf("unsupported filter field %q", left)
		}
		value := filterValue(right, filter.GetArgs())
		if value == nil {
			return nil, nil, fmt.Errorf("unsupported filter value %q", right)
		}
		quoted, err := quoteColumnName(left)
		if err != nil {
			return nil, nil, err
		}
		if op == "contains" {
			clauses = append(clauses, fmt.Sprintf("CAST(%s AS VARCHAR) LIKE ?", quoted))
			args = append(args, "%"+factvalue.String(value)+"%")
			continue
		}
		sqlOp := op
		if sqlOp == "==" {
			sqlOp = "="
		}
		if sqlOp == "!=" {
			sqlOp = "<>"
		}
		clauses = append(clauses, fmt.Sprintf("%s %s ?", quoted, sqlOp))
		args = append(args, typedSQLValue(value, valueType))
	}
	return clauses, args, nil
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

func buildOrderBy(sorts []*pb.SortSpec, columns []*pb.ResultColumn) (string, error) {
	if len(sorts) == 0 {
		return "", nil
	}
	columnTypes := resultColumnTypes(columns)
	parts := make([]string, 0, len(sorts)+3)
	for _, spec := range sorts {
		fieldName := strings.TrimSpace(spec.GetFieldName())
		if _, ok := columnTypes[fieldName]; !ok {
			return "", fmt.Errorf("unsupported sort field %q", fieldName)
		}
		direction := "ASC"
		if spec.GetDesc() {
			direction = "DESC"
		}
		quotedName, err := quoteColumnName(fieldName)
		if err != nil {
			return "", err
		}
		parts = append(parts, quotedName+" "+direction)
	}
	parts = append(parts, "subject_id ASC", "freq ASC", "data_time ASC")
	return strings.Join(parts, ", "), nil
}

func resultSelectColumns(columns []*pb.ResultColumn) ([]string, error) {
	names := []string{"space_id", "dataset_id", "subject_id", "freq", "dimensions_json", "data_time", "attributes_json"}
	for _, column := range columns {
		if column.GetColumnName() == "" {
			continue
		}
		if _, err := quoteColumnName(column.GetColumnName()); err != nil {
			return nil, err
		}
		names = append(names, column.GetColumnName())
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		quotedName, err := quoteColumnName(name)
		if err != nil {
			return nil, err
		}
		out = append(out, quotedName)
	}
	return out, nil
}

func resultSelectColumnsForRequest(columns []*pb.ResultColumn, includes []string) ([]string, error) {
	return resultSelectColumns(projectColumns(columns, includes))
}

func resultColumnTypes(columns []*pb.ResultColumn) map[string]pb.FieldValueType {
	out := map[string]pb.FieldValueType{
		"space_id":   pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
		"dataset_id": pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
		"subject_id": pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
		"freq":       pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
		"data_time":  pb.FieldValueType_FIELD_VALUE_TYPE_TIME,
	}
	for _, column := range columns {
		out[column.GetColumnName()] = column.GetValueType()
	}
	return out
}

func normalizePage(page *pb.Page) (uint32, uint32) {
	pageNo := uint32(1)
	size := defaultTimeSeriesViewPageSize
	if page != nil {
		if page.GetPage() > 0 {
			pageNo = page.GetPage()
		}
		if page.GetSize() > 0 {
			size = page.GetSize()
		}
	}
	return pageNo, size
}

func scanResultRows(rows *sql.Rows, columns []*pb.ResultColumn) ([]*pb.TimeSeriesRow, error) {
	var out []*pb.TimeSeriesRow
	for rows.Next() {
		values := make([]any, 7+len(columns))
		dest := make([]any, len(values))
		for idx := range values {
			dest[idx] = &values[idx]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		dimensions := map[string]string{}
		if raw := dbString(values[4]); raw != "" {
			if err := json.Unmarshal([]byte(raw), &dimensions); err != nil {
				return nil, err
			}
		}
		attributes := map[string]string{}
		if raw := dbString(values[6]); raw != "" {
			if err := json.Unmarshal([]byte(raw), &attributes); err != nil {
				return nil, err
			}
		}
		row := &pb.TimeSeriesRow{
			Key: &pb.TimeSeriesKey{
				SpaceId:    dbString(values[0]),
				DatasetId:  dbString(values[1]),
				SubjectId:  dbString(values[2]),
				Freq:       dbString(values[3]),
				Dimensions: dimensions,
				DataTime:   dbString(values[5]),
			},
			Attributes: attributes,
			Columns:    make([]*pb.ColumnValue, 0, len(columns)),
		}
		for idx, column := range columns {
			row.Columns = append(row.Columns, &pb.ColumnValue{
				ColumnName: column.GetColumnName(),
				ValueType:  column.GetValueType(),
				Value:      typedValueFromDB(values[7+idx], column.GetValueType()),
			})
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func typedSQLValue(value *pb.TypedValue, valueType pb.FieldValueType) any {
	if value == nil {
		return nil
	}
	return sqlValue(&pb.ColumnValue{ValueType: valueType, Value: value}, valueType)
}

func typedValueFromDB(value any, valueType pb.FieldValueType) *pb.TypedValue {
	if value == nil {
		return nil
	}
	switch valueType {
	case pb.FieldValueType_FIELD_VALUE_TYPE_INT:
		return &pb.TypedValue{Value: &pb.TypedValue_IntValue{IntValue: dbInt(value)}}
	case pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE:
		return &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: dbFloat(value)}}
	case pb.FieldValueType_FIELD_VALUE_TYPE_BOOL:
		return &pb.TypedValue{Value: &pb.TypedValue_BoolValue{BoolValue: dbBool(value)}}
	case pb.FieldValueType_FIELD_VALUE_TYPE_TIME:
		return &pb.TypedValue{Value: &pb.TypedValue_TimeValue{TimeValue: dbString(value)}}
	case pb.FieldValueType_FIELD_VALUE_TYPE_JSON:
		return &pb.TypedValue{Value: &pb.TypedValue_JsonValue{JsonValue: dbString(value)}}
	case pb.FieldValueType_FIELD_VALUE_TYPE_BYTES:
		if bytes, ok := value.([]byte); ok {
			return &pb.TypedValue{Value: &pb.TypedValue_BytesValue{BytesValue: bytes}}
		}
		return &pb.TypedValue{Value: &pb.TypedValue_BytesValue{BytesValue: []byte(dbString(value))}}
	case pb.FieldValueType_FIELD_VALUE_TYPE_STRING, pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED:
		return &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: dbString(value)}}
	default:
		return &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: dbString(value)}}
	}
}

func dbString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	default:
		return fmt.Sprint(v)
	}
}

func dbInt(value any) int64 {
	switch v := value.(type) {
	case int64:
		return v
	case int32:
		return int64(v)
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case []byte:
		n, _ := strconv.ParseInt(string(v), 10, 64)
		return n
	case string:
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	default:
		return 0
	}
}

func dbFloat(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int64:
		return float64(v)
	case int:
		return float64(v)
	case []byte:
		n, _ := strconv.ParseFloat(string(v), 64)
		return n
	case string:
		n, _ := strconv.ParseFloat(v, 64)
		return n
	default:
		return 0
	}
}

func dbBool(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case []byte:
		b, _ := strconv.ParseBool(string(v))
		return b
	case string:
		b, _ := strconv.ParseBool(v)
		return b
	default:
		return false
	}
}

func projectColumns(columns []*pb.ResultColumn, includes []string) []*pb.ResultColumn {
	if len(includes) == 0 {
		return columns
	}
	allow := stringSet(includes)
	out := make([]*pb.ResultColumn, 0, len(includes))
	for _, column := range columns {
		if allow[column.GetColumnName()] {
			out = append(out, column)
		}
	}
	return out
}

func projectRows(rows []*pb.TimeSeriesRow, includes []string) []*pb.TimeSeriesRow {
	if len(includes) == 0 {
		return rows
	}
	allow := stringSet(includes)
	out := make([]*pb.TimeSeriesRow, 0, len(rows))
	for _, row := range rows {
		projected := &pb.TimeSeriesRow{
			Key:     row.GetKey(),
			Columns: make([]*pb.ColumnValue, 0, len(includes)),
		}
		for _, value := range row.GetColumns() {
			if allow[value.GetColumnName()] {
				projected.Columns = append(projected.Columns, value)
			}
		}
		out = append(out, projected)
	}
	return out
}
