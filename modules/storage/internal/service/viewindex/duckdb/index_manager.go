//go:build cgo

package duckdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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

var identifierRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)?$`)
var duckDBMemoryLimitRE = regexp.MustCompile(`^[1-9][0-9]*(?:KB|MB|GB|TB)$`)

const (
	duckDBMemoryLimitEnv = "MOOX_STORAGE_VIEW_DUCKDB_MEMORY_LIMIT"
	duckDBThreadsEnv     = "MOOX_STORAGE_VIEW_DUCKDB_THREADS"
	defaultDuckDBMemory  = "512MB"
	defaultDuckDBThreads = 1
	defaultMaxOpenConns  = 8
)

func OpenIndexManager(opts IndexManagerOptions) (*IndexManager, error) {
	if strings.TrimSpace(opts.Root) == "" {
		return nil, errors.New("view index root is required")
	}
	if err := os.MkdirAll(opts.Root, 0o755); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(opts.Root)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".duckdb") {
			continue
		}
		db, err := open(filepath.Join(opts.Root, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("open existing duckdb view %q: %w", entry.Name(), err)
		}
		validateCtx, cancelValidate := context.WithTimeout(context.TODO(), 30*time.Second)
		validateErr := validateSystemSchema(validateCtx, db)
		cancelValidate()
		closeErr := db.Close()
		if validateErr != nil {
			return nil, fmt.Errorf("validate existing duckdb view %q: %w", entry.Name(), validateErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close existing duckdb view %q: %w", entry.Name(), closeErr)
		}
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
		"data_time TIMESTAMP_NS NOT NULL",
		"series_tag VARCHAR NOT NULL",
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
		CREATE TABLE view_rows (%s, PRIMARY KEY (subject_id, freq, data_time, series_tag));
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
		args, err := rowArgs(columns, names, write, batch.WriteMode)
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
	names := []string{"subject_id", "freq", "data_time", "series_tag"}
	for name := range columns {
		if isSystemColumn(name) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names[4:])
	return names
}

func upsertSQL(names []string, mode viewindex.WriteMode) string {
	placeholders := strings.TrimRight(strings.Repeat("?,", len(names)), ",")
	sets := make([]string, 0, len(names)-4)
	for _, name := range names[4:] {
		// Live writes overwrite complete rows. Backfill only fills missing values.
		set := fmt.Sprintf("%s = excluded.%s", quote(name), quote(name))
		if mode == viewindex.Backfill {
			set = fmt.Sprintf("%s = COALESCE(view_rows.%s, excluded.%s)", quote(name), quote(name), quote(name))
		}
		sets = append(sets, set)
	}
	conflict := "ON CONFLICT (subject_id, freq, data_time, series_tag) DO NOTHING"
	if len(sets) > 0 {
		conflict = "ON CONFLICT (subject_id, freq, data_time, series_tag) DO UPDATE SET " + strings.Join(sets, ", ")
	}
	return fmt.Sprintf("INSERT INTO view_rows (%s) VALUES (%s) %s", joinQuoted(names), placeholders, conflict)
}

func rowArgs(columns map[string]pb.FieldValueType, names []string, write viewindex.RowWrite, mode viewindex.WriteMode) ([]any, error) {
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
		value, err = normalizeColumnValue(value, columns[field.GetFieldId()], mode)
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
		value, err = normalizeColumnValue(value, columns[name], mode)
		if err != nil {
			return nil, fmt.Errorf("column %q: %w", name, err)
		}
		values[name] = value
	}
	args := make([]any, 0, len(names))
	args = append(args, key.GetSubjectId(), key.GetFreq(), when, key.GetSeriesTag())
	for _, name := range names[4:] {
		args = append(args, values[name])
	}
	return args, nil
}

func normalizeColumnValue(value any, valueType pb.FieldValueType, mode viewindex.WriteMode) (any, error) {
	if value == nil || valueType != pb.FieldValueType_FIELD_VALUE_TYPE_JSON {
		return value, nil
	}
	var raw []byte
	switch v := value.(type) {
	case string:
		raw = []byte(v)
	case []byte:
		raw = v
	default:
		return value, nil
	}
	if json.Valid(raw) {
		return value, nil
	}
	if mode == viewindex.Backfill {
		// Historical rows may contain malformed JSON from older writers. Keep
		// the time-series row during a rebuild, but do not copy the bad field.
		return nil, nil
	}
	return nil, errors.New("invalid JSON value")
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
	selectColumns := []string{"subject_id", "freq", "data_time", "series_tag"}
	projected, err := resolveIncludedColumns(columns, spec.Includes)
	if err != nil {
		return nil, err
	}
	selectColumns = append(selectColumns, projected...)
	sort.Strings(selectColumns[4:])
	query := "SELECT " + joinQuoted(selectColumns) + " FROM view_rows" + where
	query += orderSQL(spec.Sorts, spec.Order, columns)
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
		row := &pb.RowFieldValues{Key: &pb.RowKey{SpaceId: spaceID, DatasetId: datasetID, Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: valueString(values[0]), Freq: valueString(values[1]), DataTime: dataTime, SeriesTag: valueString(values[3])}}}}
		for index, name := range selectColumns[4:] {
			value := values[index+4]
			if value == nil {
				if len(spec.Includes) == 0 {
					continue
				}
				row.Fields = append(row.Fields, &pb.FieldValue{FieldId: name, Value: &pb.TypedValue{
					Value: &pb.TypedValue_NullValue{NullValue: pb.NullValue_NULL_VALUE_NULL},
				}})
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

func resolveIncludedColumns(columns map[string]pb.FieldValueType, includes []string) ([]string, error) {
	if len(includes) == 0 {
		selected := make([]string, 0, len(columns))
		for name := range columns {
			if !isSystemColumn(name) {
				selected = append(selected, name)
			}
		}
		return selected, nil
	}
	selected := make(map[string]struct{}, len(includes))
	for _, include := range includes {
		if _, ok := columns[include]; ok && !isSystemColumn(include) {
			selected[include] = struct{}{}
			continue
		}
		matches := make([]string, 0, 1)
		for name := range columns {
			if !isSystemColumn(name) && strings.HasSuffix(name, "."+include) {
				matches = append(matches, name)
			}
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("View column %q is not projected", include)
		}
		if len(matches) > 1 {
			sort.Strings(matches)
			return nil, fmt.Errorf("View column %q is ambiguous: %s", include, strings.Join(matches, ", "))
		}
		selected[matches[0]] = struct{}{}
	}
	result := make([]string, 0, len(selected))
	for name := range selected {
		result = append(result, name)
	}
	return result, nil
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

func (m *IndexManager) Exists(_ context.Context, id string) (bool, error) {
	path, err := m.path(id)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
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
	if err := validateSystemSchema(ctx, db); err != nil {
		_ = db.Close()
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
	// Factor reads arrive concurrently while the View consumer keeps applying
	// result rows. A single database/sql connection serializes all readers and
	// can starve the writer behind an entire read-worker window. DuckDB supports
	// concurrent readers and one writer within the same process; keep a modest
	// fixed pool so read concurrency is useful without mirroring the much larger
	// Factor worker count.
	db.SetMaxOpenConns(defaultMaxOpenConns)
	db.SetMaxIdleConns(defaultMaxOpenConns)
	memoryLimit := strings.TrimSpace(os.Getenv(duckDBMemoryLimitEnv))
	if memoryLimit == "" {
		memoryLimit = defaultDuckDBMemory
	}
	if !duckDBMemoryLimitRE.MatchString(memoryLimit) {
		_ = db.Close()
		return nil, fmt.Errorf("invalid %s %q", duckDBMemoryLimitEnv, memoryLimit)
	}
	threads := defaultDuckDBThreads
	if raw := strings.TrimSpace(os.Getenv(duckDBThreadsEnv)); raw != "" {
		threads, err = strconv.Atoi(raw)
		if err != nil || threads < 1 || threads > 8 {
			_ = db.Close()
			return nil, fmt.Errorf("invalid %s %q", duckDBThreadsEnv, raw)
		}
	}
	// The storage host is intentionally small. Bound every view connection and
	// spill temporary query state beside the index instead of allowing DuckDB's
	// host-sized default memory limit to compete with the other services.
	settings := fmt.Sprintf(
		"SET memory_limit = %s; SET threads = %d; SET temp_directory = %s",
		sqlStringLiteral(memoryLimit), threads, sqlStringLiteral(filepath.Dir(path)),
	)
	if _, err := db.Exec(settings); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("configure duckdb resource limits: %w", err)
	}
	return db, nil
}

func sqlStringLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func validateSystemSchema(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info('view_rows')`)
	if err != nil {
		return invalidSchemaError(err)
	}
	defer rows.Close()
	found := make(map[string]bool)
	systemTypes := map[string]string{
		"subject_id": "VARCHAR",
		"freq":       "VARCHAR",
		"data_time":  "TIMESTAMP_NS",
		"series_tag": "VARCHAR",
	}
	for rows.Next() {
		var cid int
		var notNull, primary bool
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primary); err != nil {
			return invalidSchemaError(err)
		}
		_, _ = cid, primary
		if isSystemColumn(name) {
			if !strings.EqualFold(columnType, systemTypes[name]) {
				return invalidSchemaError(fmt.Errorf("system column %q has type %s, want %s", name, columnType, systemTypes[name]))
			}
			found[name] = notNull
		}
	}
	if err := rows.Err(); err != nil {
		return invalidSchemaError(err)
	}
	for _, name := range []string{"subject_id", "freq", "data_time", "series_tag"} {
		if !found[name] {
			return invalidSchemaError(fmt.Errorf("required NOT NULL system column %q is missing", name))
		}
	}
	var primaryKey string
	if err := db.QueryRowContext(ctx, `
		SELECT array_to_string(constraint_column_names, ',')
		FROM duckdb_constraints()
		WHERE table_name = 'view_rows' AND constraint_type = 'PRIMARY KEY'`).Scan(&primaryKey); err != nil {
		return invalidSchemaError(err)
	}
	if primaryKey != "subject_id,freq,data_time,series_tag" {
		return invalidSchemaError(fmt.Errorf("primary key is (%s), want (subject_id,freq,data_time,series_tag)", primaryKey))
	}
	return nil
}

func invalidSchemaError(cause error) error {
	return fmt.Errorf("duckdb view schema is incompatible; clean the index and rebuild it: %w", cause)
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
	return name == "subject_id" || name == "freq" || name == "data_time" || name == "series_tag"
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
		return "TIMESTAMP_NS"
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
				return whereClause{}, nil, errors.New("duckdb exact query key data_time is required")
			}
			when, err := time.Parse(time.RFC3339Nano, row.GetDataTime())
			if err != nil {
				return whereClause{}, nil, err
			}
			keys = append(keys, "(subject_id = ? AND freq = ? AND data_time = ? AND series_tag = ?)")
			args = append(args, row.GetSubjectId(), row.GetFreq(), when, row.GetSeriesTag())
		}
		parts = append(parts, "("+strings.Join(keys, " OR ")+")")
	}
	if len(spec.Selectors) != 0 {
		selectors := make([]string, 0, len(spec.Selectors))
		for _, selector := range spec.Selectors {
			if selector.SubjectID == "" || selector.Freq == "" {
				return whereClause{}, nil, errors.New("duckdb selector subject_id and freq are required")
			}
			part := "(subject_id = ? AND freq = ?"
			args = append(args, selector.SubjectID, selector.Freq)
			if selector.SeriesTag != nil {
				part += " AND series_tag = ?"
				args = append(args, *selector.SeriesTag)
			}
			selectors = append(selectors, part+")")
		}
		parts = append(parts, "("+strings.Join(selectors, " OR ")+")")
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
		if hasDataTimeFirstSort(spec.Sorts) {
			parts = append(parts, "(data_time > ? OR (data_time = ? AND subject_id > ?) OR (data_time = ? AND subject_id = ? AND freq > ?) OR (data_time = ? AND subject_id = ? AND freq = ? AND series_tag > ?))")
			args = append(args,
				when,
				when, row.GetSubjectId(),
				when, row.GetSubjectId(), row.GetFreq(),
				when, row.GetSubjectId(), row.GetFreq(), row.GetSeriesTag(),
			)
		} else {
			parts = append(parts, "(subject_id > ? OR (subject_id = ? AND freq > ?) OR (subject_id = ? AND freq = ? AND data_time > ?) OR (subject_id = ? AND freq = ? AND data_time = ? AND series_tag > ?))")
			args = append(args,
				row.GetSubjectId(),
				row.GetSubjectId(), row.GetFreq(),
				row.GetSubjectId(), row.GetFreq(), when,
				row.GetSubjectId(), row.GetFreq(), when, row.GetSeriesTag(),
			)
		}
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

func hasDataTimeFirstSort(sorts []*pb.SortSpec) bool {
	return len(sorts) > 0 && sorts[0] != nil && sorts[0].GetFieldName() == "data_time" && !sorts[0].GetDesc()
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

func orderSQL(sorts []*pb.SortSpec, order pb.SortOrder, columns map[string]pb.FieldValueType) string {
	identity := []string{"subject_id", "freq", "data_time", "series_tag"}
	if len(sorts) == 0 {
		direction := "ASC"
		if order == pb.SortOrder_SORT_ORDER_DESC {
			direction = "DESC"
		}
		parts := make([]string, 0, len(identity))
		for _, name := range identity {
			parts = append(parts, quote(name)+" "+direction)
		}
		return " ORDER BY " + strings.Join(parts, ", ")
	}
	parts := make([]string, 0, len(sorts)+len(identity))
	seen := make(map[string]struct{}, len(sorts))
	for _, sortSpec := range sorts {
		if sortSpec == nil || !identifierRE.MatchString(sortSpec.GetFieldName()) {
			continue
		}
		if !isSystemColumn(sortSpec.GetFieldName()) && !containsKey(columns, sortSpec.GetFieldName()) {
			continue
		}
		if _, exists := seen[sortSpec.GetFieldName()]; exists {
			continue
		}
		direction := "ASC"
		if sortSpec.GetDesc() {
			direction = "DESC"
		}
		parts = append(parts, quote(sortSpec.GetFieldName())+" "+direction)
		seen[sortSpec.GetFieldName()] = struct{}{}
	}
	for _, name := range identity {
		if _, exists := seen[name]; !exists {
			parts = append(parts, quote(name)+" ASC")
		}
	}
	return " ORDER BY " + strings.Join(parts, ", ")
}
