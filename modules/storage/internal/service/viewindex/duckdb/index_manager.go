//go:build cgo

package duckdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type IndexManagerOptions struct{ Root string }

type IndexManager struct {
	root    string
	mu      sync.Mutex
	dbs     map[string]*sql.DB
	schema  map[string]map[string]pb.FieldValueType
	dataset map[string]string
	space   map[string]string
}

var identifierRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func OpenIndexManager(opts IndexManagerOptions) (*IndexManager, error) {
	if strings.TrimSpace(opts.Root) == "" {
		return nil, errors.New("view index root is required")
	}
	if err := os.MkdirAll(opts.Root, 0o755); err != nil {
		return nil, err
	}
	return &IndexManager{root: opts.Root, dbs: make(map[string]*sql.DB), schema: make(map[string]map[string]pb.FieldValueType), dataset: make(map[string]string), space: make(map[string]string)}, nil
}

func (m *IndexManager) Engine() string { return "duckdb" }

func (m *IndexManager) Prepare(ctx context.Context, id string, schema viewindex.ViewIndexSchema) error {
	path, err := m.path(id)
	if err != nil {
		return err
	}
	if err := m.closeIndex(id); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	db, err := open(path)
	if err != nil {
		return err
	}
	columns := schemaColumns(schema.Columns)
	createColumns := []string{
		"subject_id VARCHAR NOT NULL",
		"freq VARCHAR NOT NULL",
		"data_time TIMESTAMP NOT NULL",
	}
	for name, valueType := range columns {
		if isSystemColumn(name) {
			continue
		}
		createColumns = append(createColumns, quote(name)+" "+duckType(valueType))
	}
	statement := fmt.Sprintf(`
		CREATE TABLE view_meta (singleton INTEGER PRIMARY KEY, view_version UBIGINT NOT NULL, schema_hash VARCHAR NOT NULL, primary_dataset_id VARCHAR NOT NULL, space_id VARCHAR NOT NULL, updated_at VARCHAR NOT NULL);
		CREATE TABLE view_columns (column_name VARCHAR PRIMARY KEY, value_type INTEGER NOT NULL);
		CREATE TABLE view_rows (%s, PRIMARY KEY (subject_id, freq, data_time));
		CREATE INDEX idx_view_rows_data_time ON view_rows (data_time);`, strings.Join(createColumns, ", "))
	if _, err := db.ExecContext(ctx, statement); err != nil {
		_ = db.Close()
		return err
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO view_meta VALUES (1, ?, ?, ?, ?, ?)`, schema.ViewVersion, schema.SchemaHash, schema.PrimaryDatasetID, schema.SpaceID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		_ = db.Close()
		return err
	}
	for name, valueType := range columns {
		if _, err := db.ExecContext(ctx, `INSERT INTO view_columns VALUES (?, ?)`, name, int32(valueType)); err != nil {
			_ = db.Close()
			return err
		}
	}
	m.mu.Lock()
	m.dbs[id] = db
	m.schema[id] = columns
	m.dataset[id] = schema.PrimaryDatasetID
	m.space[id] = schema.SpaceID
	m.mu.Unlock()
	return nil
}

func (m *IndexManager) Write(ctx context.Context, id string, batch viewindex.ViewIndexWriteBatch) error {
	if err := batch.Validate(); err != nil {
		return err
	}
	db, columns, _, _, err := m.getIndex(ctx, id)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var revision uint64
	var schemaHash string
	if err := tx.QueryRowContext(ctx, `SELECT view_version, schema_hash FROM view_meta WHERE singleton = 1`).Scan(&revision, &schemaHash); err != nil {
		return err
	}
	if revision != batch.ViewRevision {
		return fmt.Errorf("view revision conflict: current=%d requested=%d", revision, batch.ViewRevision)
	}
	if schemaHash != batch.ViewSchemaHash {
		return errors.New("view schema hash conflict")
	}
	names := upsertColumnNames(columns)
	stmt, err := tx.PrepareContext(ctx, upsertSQL(names, batch.WriteMode))
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, write := range batch.RowWrites {
		args, err := rowArgs(columns, names, write)
		if err != nil {
			return err
		}
		if _, err := stmt.ExecContext(ctx, args...); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE view_meta SET updated_at = ? WHERE singleton = 1`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func upsertColumnNames(columns map[string]pb.FieldValueType) []string {
	names := []string{"subject_id", "freq", "data_time"}
	for name := range columns {
		if isSystemColumn(name) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names[3:])
	return names
}

func upsertSQL(names []string, mode viewindex.WriteMode) string {
	placeholders := strings.TrimRight(strings.Repeat("?,", len(names)), ",")
	sets := make([]string, 0, len(names)-3)
	for _, name := range names[3:] {
		// Live writes overwrite complete rows. Backfill only fills missing values.
		set := fmt.Sprintf("%s = excluded.%s", quote(name), quote(name))
		if mode == viewindex.Backfill {
			set = fmt.Sprintf("%s = COALESCE(view_rows.%s, excluded.%s)", quote(name), quote(name), quote(name))
		}
		sets = append(sets, set)
	}
	return fmt.Sprintf("INSERT INTO view_rows (%s) VALUES (%s) ON CONFLICT (subject_id, freq, data_time) DO UPDATE SET %s", joinQuoted(names), placeholders, strings.Join(sets, ", "))
}

func rowArgs(columns map[string]pb.FieldValueType, names []string, write viewindex.RowWrite) ([]any, error) {
	key := write.Key.Key.GetTimeSeries()
	if key == nil {
		return nil, errors.New("duckdb only accepts time-series row keys")
	}
	when, err := time.Parse(time.RFC3339Nano, key.GetDataTime())
	if err != nil {
		return nil, fmt.Errorf("invalid data_time: %w", err)
	}
	values := make(map[string]any, len(write.Fields)+len(write.Attributes))
	for _, field := range write.Fields {
		if field == nil {
			continue
		}
		if _, ok := columns[field.GetFieldId()]; !ok {
			return nil, fmt.Errorf("unknown view column %q", field.GetFieldId())
		}
		value, err := typedValueToDB(field.GetValue())
		if err != nil {
			return nil, fmt.Errorf("column %q: %w", field.GetFieldId(), err)
		}
		values[field.GetFieldId()] = value
	}
	for name, typed := range write.Attributes {
		if _, ok := columns[name]; !ok {
			return nil, fmt.Errorf("unknown view column %q", name)
		}
		value, err := typedValueToDB(typed)
		if err != nil {
			return nil, fmt.Errorf("column %q: %w", name, err)
		}
		values[name] = value
	}
	args := make([]any, 0, len(names))
	args = append(args, key.GetSubjectId(), key.GetFreq(), when)
	for _, name := range names[3:] {
		args = append(args, values[name])
	}
	return args, nil
}

func (m *IndexManager) Query(ctx context.Context, id string, spec viewindex.QuerySpec) ([]*pb.RowFieldValues, int64, error) {
	db, columns, datasetID, spaceID, err := m.getIndex(ctx, id)
	if err != nil {
		return nil, 0, err
	}
	where, args, err := buildWhere(spec, columns)
	if err != nil {
		return nil, 0, err
	}
	if spec.TotalMode != pb.TotalMode_NONE {
		var total int64
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM view_rows"+where.sql, where.args...).Scan(&total); err != nil {
			return nil, 0, err
		}
		args = args[:0]
		args = append(args, where.args...)
		rows, err := m.queryRows(ctx, db, columns, spaceID, datasetID, spec, where.sql, args)
		return rows, total, err
	}
	rows, err := m.queryRows(ctx, db, columns, spaceID, datasetID, spec, where.sql, args)
	return rows, -1, err
}

type whereClause struct {
	sql  string
	args []any
}

func (m *IndexManager) queryRows(ctx context.Context, db *sql.DB, columns map[string]pb.FieldValueType, spaceID, datasetID string, spec viewindex.QuerySpec, where string, args []any) ([]*pb.RowFieldValues, error) {
	selectColumns := []string{"subject_id", "freq", "data_time"}
	for name := range columns {
		if !isSystemColumn(name) && (len(spec.Includes) == 0 || contains(spec.Includes, name)) {
			selectColumns = append(selectColumns, name)
		}
	}
	sort.Strings(selectColumns[3:])
	query := "SELECT " + joinQuoted(selectColumns) + " FROM view_rows" + where
	query += orderSQL(spec.Sorts, columns)
	limit := spec.Limit
	if limit <= 0 {
		limit = 1000
	}
	if spec.Offset < 0 {
		spec.Offset = 0
	}
	query += " LIMIT ? OFFSET ?"
	args = append(args, limit, spec.Offset)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]*pb.RowFieldValues, 0)
	for rows.Next() {
		values := make([]any, len(selectColumns))
		dest := make([]any, len(values))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		dataTime, err := scanTime(values[2])
		if err != nil {
			return nil, err
		}
		row := &pb.RowFieldValues{Key: &pb.RowKey{SpaceId: spaceID, DatasetId: datasetID, Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: valueString(values[0]), Freq: valueString(values[1]), DataTime: dataTime}}}}
		for index, name := range selectColumns[3:] {
			value := values[index+3]
			if value == nil {
				continue
			}
			typed, err := dbToTypedValue(value, columns[name])
			if err != nil {
				return nil, fmt.Errorf("column %q: %w", name, err)
			}
			row.Fields = append(row.Fields, &pb.FieldValue{FieldId: name, Value: typed})
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (m *IndexManager) Stat(ctx context.Context, id string) (viewindex.ViewIndexStats, error) {
	path, err := m.path(id)
	if err != nil {
		return viewindex.ViewIndexStats{}, err
	}
	_, err = os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return viewindex.ViewIndexStats{}, nil
	}
	if err != nil {
		return viewindex.ViewIndexStats{}, err
	}
	db, _, _, _, err := m.getIndex(ctx, id)
	if err != nil {
		return viewindex.ViewIndexStats{}, err
	}
	var stats viewindex.ViewIndexStats
	if err := db.QueryRowContext(ctx, `SELECT view_version, schema_hash, updated_at FROM view_meta WHERE singleton = 1`).Scan(&stats.ViewVersion, &stats.SchemaHash, &stats.UpdatedAt); err != nil {
		return viewindex.ViewIndexStats{}, err
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM view_rows`).Scan(&stats.EntryCount); err != nil {
		return viewindex.ViewIndexStats{}, err
	}
	var from, to sql.NullTime
	if err := db.QueryRowContext(ctx, `SELECT MIN(data_time), MAX(data_time) FROM view_rows`).Scan(&from, &to); err != nil {
		return viewindex.ViewIndexStats{}, err
	}
	if from.Valid {
		stats.IndexedFrom = from.Time.UTC().Format(time.RFC3339Nano)
	}
	if to.Valid {
		stats.IndexedTo = to.Time.UTC().Format(time.RFC3339Nano)
	}
	stats.Exists = true
	return stats, nil
}

func (m *IndexManager) Remove(_ context.Context, id string) error {
	path, err := m.path(id)
	if err != nil {
		return err
	}
	if err := m.closeIndex(id); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (m *IndexManager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result error
	for id, db := range m.dbs {
		result = errors.Join(result, db.Close())
		delete(m.dbs, id)
		delete(m.schema, id)
		delete(m.dataset, id)
		delete(m.space, id)
	}
	return result
}

func (m *IndexManager) closeIndex(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if db := m.dbs[id]; db != nil {
		if err := db.Close(); err != nil {
			return err
		}
		delete(m.dbs, id)
	}
	delete(m.schema, id)
	delete(m.dataset, id)
	delete(m.space, id)
	return nil
}

func (m *IndexManager) getIndex(ctx context.Context, id string) (*sql.DB, map[string]pb.FieldValueType, string, string, error) {
	path, err := m.path(id)
	if err != nil {
		return nil, nil, "", "", err
	}
	m.mu.Lock()
	if db := m.dbs[id]; db != nil {
		columns := m.schema[id]
		m.mu.Unlock()
		return db, columns, m.dataset[id], m.space[id], nil
	}
	m.mu.Unlock()
	if _, err := os.Stat(path); err != nil {
		return nil, nil, "", "", err
	}
	db, err := open(path)
	if err != nil {
		return nil, nil, "", "", err
	}
	columns := make(map[string]pb.FieldValueType)
	rows, err := db.QueryContext(ctx, `SELECT column_name, value_type FROM view_columns`)
	if err != nil {
		_ = db.Close()
		return nil, nil, "", "", err
	}
	for rows.Next() {
		var name string
		var valueType int32
		if err := rows.Scan(&name, &valueType); err != nil {
			_ = rows.Close()
			_ = db.Close()
			return nil, nil, "", "", err
		}
		columns[name] = pb.FieldValueType(valueType)
	}
	if err := rows.Close(); err != nil {
		_ = db.Close()
		return nil, nil, "", "", err
	}
	var datasetID, spaceID string
	if err := db.QueryRowContext(ctx, `SELECT primary_dataset_id, space_id FROM view_meta WHERE singleton = 1`).Scan(&datasetID, &spaceID); err != nil {
		_ = db.Close()
		return nil, nil, "", "", err
	}
	m.mu.Lock()
	if existing := m.dbs[id]; existing != nil {
		_ = db.Close()
		return existing, m.schema[id], m.dataset[id], m.space[id], nil
	}
	m.dbs[id], m.schema[id], m.dataset[id] = db, columns, datasetID
	m.space[id] = spaceID
	m.mu.Unlock()
	return db, columns, datasetID, spaceID, nil
}

func (m *IndexManager) path(id string) (string, error) {
	if id == "" || filepath.Base(id) != id {
		return "", errors.New("invalid view index id")
	}
	return filepath.Join(m.root, id+".duckdb"), nil
}

func open(path string) (*sql.DB, error) {
	db, err := sql.Open("duckdb", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db, nil
}

func schemaColumns(columns []*pb.ViewColumn) map[string]pb.FieldValueType {
	out := make(map[string]pb.FieldValueType, len(columns))
	for _, column := range columns {
		if column == nil || !identifierRE.MatchString(column.GetColumnName()) || isSystemColumn(column.GetColumnName()) {
			continue
		}
		out[column.GetColumnName()] = column.GetValueType()
	}
	return out
}

func isSystemColumn(name string) bool {
	return name == "subject_id" || name == "freq" || name == "data_time"
}

func duckType(valueType pb.FieldValueType) string {
	switch valueType {
	case pb.FieldValueType_FIELD_VALUE_TYPE_INT:
		return "BIGINT"
	case pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE:
		return "DOUBLE"
	case pb.FieldValueType_FIELD_VALUE_TYPE_BOOL:
		return "BOOLEAN"
	case pb.FieldValueType_FIELD_VALUE_TYPE_TIME:
		return "TIMESTAMP"
	case pb.FieldValueType_FIELD_VALUE_TYPE_BYTES:
		return "BLOB"
	case pb.FieldValueType_FIELD_VALUE_TYPE_JSON:
		return "JSON"
	case pb.FieldValueType_FIELD_VALUE_TYPE_STRING:
		return "VARCHAR"
	default:
		log.Printf("duckdb view index: unknown field value type %s, falling back to VARCHAR", valueType)
		return "VARCHAR"
	}
}

func quote(name string) string {
	if !identifierRE.MatchString(name) {
		return "\"invalid_column\""
	}
	return "\"" + name + "\""
}

func joinQuoted(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = quote(name)
	}
	return strings.Join(quoted, ", ")
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func valueString(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case []byte:
		return string(value)
	default:
		return fmt.Sprint(value)
	}
}

func scanTime(value any) (string, error) {
	switch value := value.(type) {
	case time.Time:
		return value.UTC().Format(time.RFC3339Nano), nil
	case string:
		return value, nil
	case []byte:
		return string(value), nil
	default:
		return "", fmt.Errorf("unexpected timestamp type %T", value)
	}
}

func typedValueToDB(value *pb.TypedValue) (any, error) {
	if value == nil {
		return nil, nil
	}
	switch v := value.GetValue().(type) {
	case *pb.TypedValue_StringValue:
		return v.StringValue, nil
	case *pb.TypedValue_IntValue:
		return v.IntValue, nil
	case *pb.TypedValue_DoubleValue:
		return v.DoubleValue, nil
	case *pb.TypedValue_BoolValue:
		return v.BoolValue, nil
	case *pb.TypedValue_TimeValue:
		return time.Parse(time.RFC3339Nano, v.TimeValue)
	case *pb.TypedValue_JsonValue:
		return v.JsonValue, nil
	case *pb.TypedValue_BytesValue:
		return v.BytesValue, nil
	case *pb.TypedValue_NullValue:
		return nil, nil
	default:
		return nil, errors.New("unsupported typed value")
	}
}

func dbToTypedValue(value any, valueType pb.FieldValueType) (*pb.TypedValue, error) {
	switch valueType {
	case pb.FieldValueType_FIELD_VALUE_TYPE_INT:
		var number int64
		switch v := value.(type) {
		case int64:
			number = v
		case int32:
			number = int64(v)
		case float64:
			number = int64(v)
		default:
			return nil, fmt.Errorf("unexpected int type %T", value)
		}
		return &pb.TypedValue{Value: &pb.TypedValue_IntValue{IntValue: number}}, nil
	case pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE:
		var number float64
		switch v := value.(type) {
		case float64:
			number = v
		case float32:
			number = float64(v)
		case int64:
			number = float64(v)
		default:
			return nil, fmt.Errorf("unexpected double type %T", value)
		}
		return &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: number}}, nil
	case pb.FieldValueType_FIELD_VALUE_TYPE_BOOL:
		v, ok := value.(bool)
		if !ok {
			return nil, fmt.Errorf("unexpected bool type %T", value)
		}
		return &pb.TypedValue{Value: &pb.TypedValue_BoolValue{BoolValue: v}}, nil
	case pb.FieldValueType_FIELD_VALUE_TYPE_TIME:
		value, err := scanTime(value)
		if err != nil {
			return nil, err
		}
		return &pb.TypedValue{Value: &pb.TypedValue_TimeValue{TimeValue: value}}, nil
	case pb.FieldValueType_FIELD_VALUE_TYPE_BYTES:
		return &pb.TypedValue{Value: &pb.TypedValue_BytesValue{BytesValue: append([]byte(nil), value.([]byte)...)}}, nil
	default:
		return &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: valueString(value)}}, nil
	}
}

func buildWhere(spec viewindex.QuerySpec, columns map[string]pb.FieldValueType) (whereClause, []any, error) {
	parts := make([]string, 0)
	args := make([]any, 0)
	if len(spec.Keys) != 0 {
		keys := make([]string, 0, len(spec.Keys))
		for _, key := range spec.Keys {
			if key == nil || key.GetTimeSeries() == nil {
				return whereClause{}, nil, errors.New("duckdb query key must be time-series")
			}
			row := key.GetTimeSeries()
			if row.GetDataTime() == "" {
				keys = append(keys, "(subject_id = ? AND freq = ?)")
				args = append(args, row.GetSubjectId(), row.GetFreq())
				continue
			}
			when, err := time.Parse(time.RFC3339Nano, row.GetDataTime())
			if err != nil {
				return whereClause{}, nil, err
			}
			keys = append(keys, "(subject_id = ? AND freq = ? AND data_time = ?)")
			args = append(args, row.GetSubjectId(), row.GetFreq(), when)
		}
		parts = append(parts, "("+strings.Join(keys, " OR ")+")")
	}
	if spec.TimeRange != nil {
		if value := spec.TimeRange.GetStartTime(); value != "" {
			when, err := time.Parse(time.RFC3339Nano, value)
			if err != nil {
				return whereClause{}, nil, err
			}
			parts = append(parts, "data_time >= ?")
			args = append(args, when)
		}
		if value := spec.TimeRange.GetEndTime(); value != "" {
			when, err := time.Parse(time.RFC3339Nano, value)
			if err != nil {
				return whereClause{}, nil, err
			}
			parts = append(parts, "data_time < ?")
			args = append(args, when)
		}
	}
	if after := spec.AfterKey; after != nil {
		row := after.GetTimeSeries()
		if row == nil {
			return whereClause{}, nil, errors.New("duckdb cursor key must be time-series")
		}
		when, err := time.Parse(time.RFC3339Nano, row.GetDataTime())
		if err != nil {
			return whereClause{}, nil, err
		}
		parts = append(parts, "(subject_id > ? OR (subject_id = ? AND freq > ?) OR (subject_id = ? AND freq = ? AND data_time > ?))")
		args = append(args, row.GetSubjectId(), row.GetSubjectId(), row.GetFreq(), row.GetSubjectId(), row.GetFreq(), when)
	}
	filter, filterArgs, err := filterSQL(spec.Groups, spec.GroupLogical, columns)
	if err != nil {
		return whereClause{}, nil, err
	}
	if filter != "" {
		parts = append(parts, filter)
		args = append(args, filterArgs...)
	}
	if len(parts) == 0 {
		return whereClause{}, args, nil
	}
	return whereClause{sql: " WHERE " + strings.Join(parts, " AND "), args: args}, args, nil
}

func filterSQL(groups []viewindex.FilterGroup, groupLogical pb.FilterLogical, columns map[string]pb.FieldValueType) (string, []any, error) {
	if len(groups) == 0 {
		return "", nil, nil
	}
	groupSQL := make([]string, 0, len(groups))
	args := make([]any, 0)
	for _, group := range groups {
		conds := make([]string, 0, len(group.Conds))
		for _, cond := range group.Conds {
			if !identifierRE.MatchString(cond.Column) || (!isSystemColumn(cond.Column) && columns[cond.Column] == pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED && !containsKey(columns, cond.Column)) {
				return "", nil, fmt.Errorf("unknown or invalid filter column %q", cond.Column)
			}
			part, values, err := conditionSQL(cond, columns[cond.Column])
			if err != nil {
				return "", nil, err
			}
			conds = append(conds, part)
			args = append(args, values...)
		}
		if len(conds) == 0 {
			continue
		}
		join := " AND "
		if group.Logical == pb.FilterLogical_FILTER_LOGICAL_OR {
			join = " OR "
		}
		groupSQL = append(groupSQL, "("+strings.Join(conds, join)+")")
	}
	if len(groupSQL) == 0 {
		return "", args, nil
	}
	join := " AND "
	if groupLogical == pb.FilterLogical_FILTER_LOGICAL_OR {
		join = " OR "
	}
	return "(" + strings.Join(groupSQL, join) + ")", args, nil
}

func containsKey(columns map[string]pb.FieldValueType, name string) bool {
	_, ok := columns[name]
	return ok
}

func conditionSQL(cond viewindex.Filter, valueType pb.FieldValueType) (string, []any, error) {
	if len(cond.Values) == 0 {
		return "", nil, errors.New("filter values are required")
	}
	values := make([]any, 0, len(cond.Values))
	for _, value := range cond.Values {
		dbValue, err := typedValueToDB(value)
		if err != nil {
			return "", nil, err
		}
		values = append(values, dbValue)
	}
	column := quote(cond.Column)
	switch cond.Op {
	case pb.FilterOp_FILTER_OP_EQ, pb.FilterOp_FILTER_OP_NE, pb.FilterOp_FILTER_OP_GT, pb.FilterOp_FILTER_OP_GTE, pb.FilterOp_FILTER_OP_LT, pb.FilterOp_FILTER_OP_LTE:
		if len(values) != 1 {
			return "", nil, errors.New("comparison filter requires one value")
		}
		if values[0] == nil {
			if cond.Op == pb.FilterOp_FILTER_OP_EQ {
				return column + " IS NULL", nil, nil
			}
			if cond.Op == pb.FilterOp_FILTER_OP_NE {
				return column + " IS NOT NULL", nil, nil
			}
			return "", nil, errors.New("null filter only supports equality or inequality")
		}
		operator := map[pb.FilterOp]string{pb.FilterOp_FILTER_OP_EQ: "=", pb.FilterOp_FILTER_OP_NE: "!=", pb.FilterOp_FILTER_OP_GT: ">", pb.FilterOp_FILTER_OP_GTE: ">=", pb.FilterOp_FILTER_OP_LT: "<", pb.FilterOp_FILTER_OP_LTE: "<="}[cond.Op]
		return column + " " + operator + " ?", values, nil
	case pb.FilterOp_FILTER_OP_IN, pb.FilterOp_FILTER_OP_NOT_IN:
		marks := strings.TrimRight(strings.Repeat("?,", len(values)), ",")
		operator := "IN"
		if cond.Op == pb.FilterOp_FILTER_OP_NOT_IN {
			operator = "NOT IN"
		}
		return fmt.Sprintf("%s %s (%s)", column, operator, marks), values, nil
	case pb.FilterOp_FILTER_OP_LIKE:
		if len(values) != 1 {
			return "", nil, errors.New("like filter requires one value")
		}
		// Substring match: LIKE '%' || ? || '%' with the bound literal.
		return column + " LIKE '%' || ? || '%'", values, nil
	case pb.FilterOp_FILTER_OP_NOT_LIKE:
		if len(values) != 1 {
			return "", nil, errors.New("not-like filter requires one value")
		}
		return column + " NOT LIKE '%' || ? || '%'", values, nil
	case pb.FilterOp_FILTER_OP_BETWEEN:
		if len(values) != 2 {
			return "", nil, errors.New("between filter requires two values")
		}
		return column + " BETWEEN ? AND ?", values, nil
	default:
		return "", nil, fmt.Errorf("unsupported filter operator %s", cond.Op)
	}
}

func orderSQL(sorts []*pb.SortSpec, columns map[string]pb.FieldValueType) string {
	parts := make([]string, 0, len(sorts))
	for _, sortSpec := range sorts {
		if sortSpec == nil || !identifierRE.MatchString(sortSpec.GetFieldName()) {
			continue
		}
		if !isSystemColumn(sortSpec.GetFieldName()) && !containsKey(columns, sortSpec.GetFieldName()) {
			continue
		}
		direction := "ASC"
		if sortSpec.GetDesc() {
			direction = "DESC"
		}
		parts = append(parts, quote(sortSpec.GetFieldName())+" "+direction)
	}
	if len(parts) == 0 {
		parts = append(parts, `"data_time" ASC`)
	}
	return " ORDER BY " + strings.Join(parts, ", ")
}
