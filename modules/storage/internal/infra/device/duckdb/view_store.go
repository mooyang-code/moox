//go:build cgo

package duckdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"
	"github.com/mooyang-code/moox/modules/storage/internal/core/factvalue"
	"github.com/mooyang-code/moox/modules/storage/internal/core/viewindex"
	"github.com/mooyang-code/moox/modules/storage/internal/infra/device/factkey"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	trpc "trpc.group/trpc-go/trpc-go"
)

// Options 保存 DuckDB 视图存储打开配置。
type Options struct {
	Path         string
	MaxOpenConns int
}

// ViewStore 封装 TimeSeries 视图在 DuckDB 中的物化读写能力。
type ViewStore struct {
	db            *sql.DB
	tableLocks    sync.Map
	indexedTables sync.Map
	writeMu       sync.Mutex
}

var tableNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var unsafeIndexNameChar = regexp.MustCompile(`[^A-Za-z0-9_]+`)

var resultBaseColumns = []string{
	"row_key",
	"space_id",
	"dataset_id",
	"subject_id",
	"freq",
	"dimensions_json",
	"data_time",
	"attributes_json",
	"row_json",
}

const (
	defaultMaxOpenConns             = 1
	defaultDuckDBMemoryLimit        = "512MB"
	defaultDuckDBThreads            = "1"
	defaultDuckDBMaxTempDirSize     = "2GB"
	duckDBMemoryLimitEnv            = "MOOX_DUCKDB_MEMORY_LIMIT"
	duckDBThreadsEnv                = "MOOX_DUCKDB_THREADS"
	duckDBMaxTempDirectorySizeEnv   = "MOOX_DUCKDB_MAX_TEMP_DIRECTORY_SIZE"
	duckDBMemoryLimitParam          = "memory_limit"
	duckDBThreadsParam              = "threads"
	duckDBMaxTempDirectorySizeParam = "max_temp_directory_size"
)

func Open(opts Options) (*ViewStore, error) {
	if opts.Path == "" {
		return nil, errors.New("duckdb path is required")
	}
	db, err := sql.Open("duckdb", duckDBDSN(opts.Path))
	if err != nil {
		return nil, err
	}
	maxOpenConns := opts.MaxOpenConns
	if maxOpenConns <= 0 {
		maxOpenConns = defaultMaxOpenConns
	}
	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxOpenConns)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	store := &ViewStore{db: db}
	if err := store.init(trpc.BackgroundContext()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func duckDBDSN(path string) string {
	return appendDuckDBParams(path, map[string]string{
		duckDBMemoryLimitParam:          duckDBConfigValue(duckDBMemoryLimitEnv, defaultDuckDBMemoryLimit),
		duckDBThreadsParam:              duckDBConfigValue(duckDBThreadsEnv, defaultDuckDBThreads),
		duckDBMaxTempDirectorySizeParam: duckDBConfigValue(duckDBMaxTempDirectorySizeEnv, defaultDuckDBMaxTempDirSize),
	})
}

func appendDuckDBParams(dsn string, params map[string]string) string {
	out := dsn
	for _, key := range []string{duckDBMemoryLimitParam, duckDBThreadsParam, duckDBMaxTempDirectorySizeParam} {
		value := strings.TrimSpace(params[key])
		if value == "" || duckDBDSNHasParam(out, key) {
			continue
		}
		separator := "?"
		if strings.Contains(out, "?") {
			separator = "&"
		}
		out += separator + key + "=" + url.QueryEscape(value)
	}
	return out
}

func duckDBConfigValue(envKey string, defaultValue string) string {
	if value := strings.TrimSpace(os.Getenv(envKey)); value != "" {
		return value
	}
	return defaultValue
}

func duckDBDSNHasParam(dsn string, name string) bool {
	queryStart := strings.Index(dsn, "?")
	if queryStart < 0 {
		return false
	}
	for _, part := range strings.Split(dsn[queryStart+1:], "&") {
		key, _, _ := strings.Cut(part, "=")
		if key == name {
			return true
		}
	}
	return false
}

func (s *ViewStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *ViewStore) Engine() string {
	return "duckdb"
}

func (s *ViewStore) Prepare(ctx context.Context, indexID string, schema viewindex.ViewIndexSchema) error {
	if err := s.DropResultTable(ctx, indexID); err != nil {
		return err
	}
	if err := s.CreateResultTable(ctx, indexID, schema.Columns); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE moox_view_index_meta SET view_version = ?, schema_hash = ?, updated_at = ? WHERE table_name = ?
	`, schema.ViewVersion, schema.SchemaHash, time.Now().UTC().Format(time.RFC3339Nano), indexID)
	return err
}

func (s *ViewStore) Write(ctx context.Context, indexID string, batch viewindex.ViewIndexBatch) error {
	if len(batch.RecordRows) > 0 {
		return fmt.Errorf("duckdb view index rejects record rows")
	}
	return s.InsertRows(ctx, indexID, batch.TimeSeriesRows)
}

func (s *ViewStore) Stat(ctx context.Context, indexID string) (viewindex.ViewIndexStats, error) {
	exists, err := s.tableExists(ctx, indexID)
	if err != nil || !exists {
		return viewindex.ViewIndexStats{Exists: exists}, err
	}
	stats, err := s.indexMeta(ctx, indexID)
	if err != nil {
		return viewindex.ViewIndexStats{}, err
	}
	return viewindex.ViewIndexStats{
		Exists:      true,
		ViewVersion: stats.viewVersion,
		EntryCount:  stats.entryCount,
		MinVersion:  stats.minVersion,
		MaxVersion:  stats.maxVersion,
		SchemaHash:  stats.schemaHash,
		UpdatedAt:   stats.updatedAt,
	}, nil
}

func (s *ViewStore) Remove(ctx context.Context, indexID string) error {
	return s.DropResultTable(ctx, indexID)
}

func (s *ViewStore) init(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS moox_view_columns (
			table_name VARCHAR PRIMARY KEY,
			columns_json VARCHAR NOT NULL
		);
		CREATE TABLE IF NOT EXISTS moox_view_index_meta (
			table_name VARCHAR PRIMARY KEY,
			view_version UBIGINT NOT NULL DEFAULT 0,
			entry_count BIGINT NOT NULL DEFAULT 0,
			min_version VARCHAR NOT NULL DEFAULT '',
			max_version VARCHAR NOT NULL DEFAULT '',
			schema_hash VARCHAR NOT NULL DEFAULT '',
			updated_at VARCHAR NOT NULL DEFAULT ''
		);
		ALTER TABLE moox_view_index_meta ADD COLUMN IF NOT EXISTS view_version UBIGINT DEFAULT 0;
	`)
	return err
}

func (s *ViewStore) CreateResultTable(ctx context.Context, tableName string, columns []*pb.ViewColumn) error {
	quoted, err := quoteTableName(tableName)
	if err != nil {
		return err
	}
	columnDefs, err := resultColumnDefs(columns)
	if err != nil {
		return err
	}
	unlock := s.lockResultTable(tableName)
	defer unlock()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	defs := []string{
		"row_key VARCHAR NOT NULL",
		"space_id VARCHAR NOT NULL",
		"dataset_id VARCHAR NOT NULL",
		"subject_id VARCHAR NOT NULL",
		"freq VARCHAR NOT NULL",
		"dimensions_json VARCHAR NOT NULL",
		"data_time VARCHAR NOT NULL",
		"attributes_json VARCHAR NOT NULL",
		"row_json VARCHAR NOT NULL",
	}
	for _, def := range columnDefs {
		quotedName, err := quoteColumnName(def.name)
		if err != nil {
			return err
		}
		defs = append(defs, quotedName+" "+duckDBType(def.valueType))
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			%s
		)
	`, quoted, strings.Join(defs, ",\n\t\t\t"))); err != nil {
		return err
	}
	if err := s.createResultIndexes(ctx, tableName, columnDefs); err != nil {
		return err
	}
	s.indexedTables.Store(tableName, struct{}{})
	encoded, err := encodeColumns(columns)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO moox_view_columns (table_name, columns_json)
		VALUES (?, ?)
		ON CONFLICT(table_name) DO UPDATE SET columns_json = excluded.columns_json
	`, tableName, encoded)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO moox_view_index_meta (table_name, updated_at)
		VALUES (?, ?)
		ON CONFLICT(table_name) DO NOTHING
	`, tableName, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

type resultColumnDef struct {
	name      string
	valueType pb.FieldValueType
}

func resultColumnDefs(columns []*pb.ViewColumn) ([]resultColumnDef, error) {
	seen := make(map[string]bool, len(columns))
	out := make([]resultColumnDef, 0, len(columns))
	for _, column := range columns {
		name := strings.TrimSpace(column.GetColumnName())
		if name == "" {
			return nil, errors.New("view column_name is required")
		}
		if _, err := quoteColumnName(name); err != nil {
			return nil, err
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, resultColumnDef{name: name, valueType: column.GetValueType()})
	}
	return out, nil
}

func resultColumnDefsFromResultColumns(columns []*pb.ResultColumn) ([]resultColumnDef, error) {
	seen := make(map[string]bool, len(columns))
	out := make([]resultColumnDef, 0, len(columns))
	for _, column := range columns {
		name := strings.TrimSpace(column.GetColumnName())
		if name == "" {
			return nil, errors.New("view column_name is required")
		}
		if _, err := quoteColumnName(name); err != nil {
			return nil, err
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, resultColumnDef{name: name, valueType: column.GetValueType()})
	}
	return out, nil
}

func (s *ViewStore) createResultIndexes(ctx context.Context, tableName string, columns []resultColumnDef) error {
	statements, err := createResultIndexStatements(tableName, columns)
	if err != nil {
		return err
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (s *ViewStore) ensureResultIndexes(ctx context.Context, tableName string, columns []*pb.ResultColumn) error {
	if _, ok := s.indexedTables.Load(tableName); ok {
		return nil
	}
	columnDefs, err := resultColumnDefsFromResultColumns(columns)
	if err != nil {
		return err
	}
	unlock := s.lockResultTable(tableName)
	defer unlock()
	if _, ok := s.indexedTables.Load(tableName); ok {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.createResultIndexes(ctx, tableName, columnDefs); err != nil {
		return err
	}
	s.indexedTables.Store(tableName, struct{}{})
	return nil
}

func createResultIndexStatements(tableName string, columns []resultColumnDef) ([]string, error) {
	quotedTable, err := quoteTableName(tableName)
	if err != nil {
		return nil, err
	}
	keyTimeIndex, err := quoteIndexName(tableName, "key_time")
	if err != nil {
		return nil, err
	}
	subjectFreqIndex, err := quoteIndexName(tableName, "subject_freq_time")
	if err != nil {
		return nil, err
	}
	dataTimeIndex, err := quoteIndexName(tableName, "data_time")
	if err != nil {
		return nil, err
	}
	statements := []string{
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (row_key, data_time)`, keyTimeIndex, quotedTable),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (subject_id, freq, data_time)`, subjectFreqIndex, quotedTable),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (data_time)`, dataTimeIndex, quotedTable),
	}
	for _, column := range columns {
		indexName, err := quoteIndexName(tableName, column.name)
		if err != nil {
			return nil, err
		}
		columnName, err := quoteColumnName(column.name)
		if err != nil {
			return nil, err
		}
		statements = append(statements, fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (%s)`,
			indexName,
			quotedTable,
			columnName,
		))
	}
	return statements, nil
}

func dropResultIndexStatements(tableName string, columns []resultColumnDef) ([]string, error) {
	keyTimeIndex, err := quoteIndexName(tableName, "key_time")
	if err != nil {
		return nil, err
	}
	subjectFreqIndex, err := quoteIndexName(tableName, "subject_freq_time")
	if err != nil {
		return nil, err
	}
	dataTimeIndex, err := quoteIndexName(tableName, "data_time")
	if err != nil {
		return nil, err
	}
	indexNames := []string{keyTimeIndex, subjectFreqIndex, dataTimeIndex}
	for _, column := range columns {
		indexName, err := quoteIndexName(tableName, column.name)
		if err != nil {
			return nil, err
		}
		indexNames = append(indexNames, indexName)
	}
	statements := make([]string, 0, len(indexNames))
	for _, indexName := range indexNames {
		statements = append(statements, fmt.Sprintf(`DROP INDEX IF EXISTS %s`, indexName))
	}
	return statements, nil
}

func (s *ViewStore) InsertRows(ctx context.Context, tableName string, rows []*pb.TimeSeriesRow) error {
	quoted, err := quoteTableName(tableName)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	unlock := s.lockResultTable(tableName)
	defer unlock()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	columns, err := s.loadColumns(ctx, tableName)
	if err != nil {
		return err
	}
	empty, err := s.resultTableEmpty(ctx, quoted)
	if err != nil {
		return err
	}
	if empty {
		_, err = s.insertRowsIntoEmptyTable(ctx, quoted, columns, rows)
	} else {
		_, err = s.mergeRowsIntoTable(ctx, quoted, columns, rows)
	}
	return err
}

func (s *ViewStore) lockResultTable(tableName string) func() {
	actual, _ := s.tableLocks.LoadOrStore(tableName, &sync.Mutex{})
	mu := actual.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (s *ViewStore) resultTableEmpty(ctx context.Context, quotedTableName string) (bool, error) {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT 1 FROM %s LIMIT 1`, quotedTableName))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	return !rows.Next(), rows.Err()
}

type persistedIndexMeta struct {
	viewVersion uint64
	entryCount  int64
	minVersion  string
	maxVersion  string
	schemaHash  string
	updatedAt   string
}

func (s *ViewStore) indexMeta(ctx context.Context, tableName string) (persistedIndexMeta, error) {
	var meta persistedIndexMeta
	err := s.db.QueryRowContext(ctx, `
		SELECT view_version, entry_count, min_version, max_version, schema_hash, updated_at
		FROM moox_view_index_meta WHERE table_name = ?
	`, tableName).Scan(&meta.viewVersion, &meta.entryCount, &meta.minVersion, &meta.maxVersion, &meta.schemaHash, &meta.updatedAt)
	return meta, err
}

func updateIndexMetaTx(ctx context.Context, tx *sql.Tx, tableName string, delta int64, minVersion string, maxVersion string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := tx.ExecContext(ctx, `
		INSERT INTO moox_view_index_meta (table_name, entry_count, min_version, max_version, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(table_name) DO UPDATE SET
			entry_count = moox_view_index_meta.entry_count + excluded.entry_count,
			min_version = CASE
				WHEN moox_view_index_meta.min_version = '' THEN excluded.min_version
				WHEN excluded.min_version = '' THEN moox_view_index_meta.min_version
				WHEN excluded.min_version < moox_view_index_meta.min_version THEN excluded.min_version
				ELSE moox_view_index_meta.min_version END,
			max_version = CASE
				WHEN moox_view_index_meta.max_version = '' THEN excluded.max_version
				WHEN excluded.max_version = '' THEN moox_view_index_meta.max_version
				WHEN excluded.max_version > moox_view_index_meta.max_version THEN excluded.max_version
				ELSE moox_view_index_meta.max_version END,
			updated_at = excluded.updated_at
	`, tableName, delta, minVersion, maxVersion, now)
	return err
}

func timeSeriesRowsVersionBounds(rows []*pb.TimeSeriesRow) (string, string) {
	var minVersion, maxVersion string
	for _, row := range rows {
		version := normalizeRowDataTime(row)
		if version == "" {
			continue
		}
		if minVersion == "" || version < minVersion {
			minVersion = version
		}
		if maxVersion == "" || version > maxVersion {
			maxVersion = version
		}
	}
	return minVersion, maxVersion
}

func (s *ViewStore) insertRowsIntoEmptyTable(ctx context.Context, quotedTableName string, columns []*pb.ResultColumn, rows []*pb.TimeSeriesRow) ([]*pb.TimeSeriesRow, error) {
	merged := mergeRowsByPrimaryKey(rows)
	tableName, err := unquoteTableName(quotedTableName)
	if err != nil {
		return nil, err
	}
	columnDefs, err := resultColumnDefsFromResultColumns(columns)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	dropStatements, err := dropResultIndexStatements(tableName, columnDefs)
	if err != nil {
		return nil, err
	}
	for _, statement := range dropStatements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
	}
	insertSQL, err := buildInsertSQL(quotedTableName, columns)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	insertStmt, err := tx.PrepareContext(ctx, insertSQL)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	defer insertStmt.Close()
	for _, row := range merged {
		args, err := resultRowArgs(row, columns)
		if err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		if _, err := insertStmt.ExecContext(ctx, args...); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
	}
	indexStatements, err := createResultIndexStatements(tableName, columnDefs)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	for _, statement := range indexStatements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
	}
	minVersion, maxVersion := timeSeriesRowsVersionBounds(merged)
	if err := updateIndexMetaTx(ctx, tx, tableName, int64(len(merged)), minVersion, maxVersion); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return merged, nil
}

func mergeRowsByPrimaryKey(rows []*pb.TimeSeriesRow) []*pb.TimeSeriesRow {
	positions := make(map[string]int, len(rows))
	out := make([]*pb.TimeSeriesRow, 0, len(rows))
	for _, row := range rows {
		key := timeSeriesRowKey(row) + "|" + normalizeRowDataTime(row)
		if idx, ok := positions[key]; ok {
			out[idx] = mergeTimeSeriesRow(out[idx], row)
			continue
		}
		positions[key] = len(out)
		out = append(out, normalizeTimeSeriesRow(row))
	}
	return out
}

func (s *ViewStore) mergeRowsIntoTable(ctx context.Context, quotedTableName string, columns []*pb.ResultColumn, rows []*pb.TimeSeriesRow) ([]*pb.TimeSeriesRow, error) {
	mergedRows := mergeRowsByPrimaryKey(rows)
	if len(mergedRows) == 0 {
		return nil, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	existing, err := loadExistingRows(ctx, tx, quotedTableName, mergedRows)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := deleteRowsByPrimaryKey(ctx, tx, quotedTableName, mergedRows); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	insertSQL, err := buildInsertSQL(quotedTableName, columns)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	insertStmt, err := tx.PrepareContext(ctx, insertSQL)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	defer insertStmt.Close()
	syncedRows := make([]*pb.TimeSeriesRow, 0, len(mergedRows))
	for _, row := range mergedRows {
		merged := row
		if existingRaw := existing[rowPrimaryKey(row)]; existingRaw != "" {
			base := &pb.TimeSeriesRow{}
			if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(existingRaw), base); err != nil {
				_ = tx.Rollback()
				return nil, err
			}
			merged = mergeTimeSeriesRow(base, row)
		}
		args, err := resultRowArgs(merged, columns)
		if err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		if _, err := insertStmt.ExecContext(ctx, args...); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		syncedRows = append(syncedRows, normalizeTimeSeriesRow(merged))
	}
	newRows := int64(len(mergedRows) - len(existing))
	minVersion, maxVersion := timeSeriesRowsVersionBounds(syncedRows)
	tableName, err := unquoteTableName(quotedTableName)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := updateIndexMetaTx(ctx, tx, tableName, newRows, minVersion, maxVersion); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return syncedRows, nil
}

const rowKeyPredicateChunkSize = 200

func loadExistingRows(ctx context.Context, tx *sql.Tx, quotedTableName string, rows []*pb.TimeSeriesRow) (map[string]string, error) {
	out := make(map[string]string, len(rows))
	for start := 0; start < len(rows); start += rowKeyPredicateChunkSize {
		end := start + rowKeyPredicateChunkSize
		if end > len(rows) {
			end = len(rows)
		}
		where, args := rowKeyPredicate(rows[start:end])
		if where == "" {
			continue
		}
		query := fmt.Sprintf(`SELECT row_key, data_time, row_json FROM %s WHERE %s`, quotedTableName, where)
		resultRows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		for resultRows.Next() {
			var rowKey, dataTime, raw string
			if err := resultRows.Scan(&rowKey, &dataTime, &raw); err != nil {
				_ = resultRows.Close()
				return nil, err
			}
			out[rowKey+"|"+dataTime] = raw
		}
		if err := resultRows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func deleteRowsByPrimaryKey(ctx context.Context, tx *sql.Tx, quotedTableName string, rows []*pb.TimeSeriesRow) error {
	for start := 0; start < len(rows); start += rowKeyPredicateChunkSize {
		end := start + rowKeyPredicateChunkSize
		if end > len(rows) {
			end = len(rows)
		}
		where, args := rowKeyPredicate(rows[start:end])
		if where == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE %s`, quotedTableName, where), args...); err != nil {
			return err
		}
	}
	return nil
}

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

func timeSeriesRowKey(row *pb.TimeSeriesRow) string {
	key := row.GetKey()
	return key.GetDatasetId() + "|" + factkey.BuildTimeSeriesDataKey(key.GetSubjectId(), key.GetFreq(), key.GetDimensions())
}

func rowPrimaryKey(row *pb.TimeSeriesRow) string {
	return timeSeriesRowKey(row) + "|" + normalizeRowDataTime(row)
}

func mergeTimeSeriesRow(base *pb.TimeSeriesRow, patch *pb.TimeSeriesRow) *pb.TimeSeriesRow {
	if base == nil {
		return normalizeTimeSeriesRow(patch)
	}
	merged := proto.Clone(base).(*pb.TimeSeriesRow)
	merged.Key = normalizeTimeSeriesRow(patch).GetKey()
	positions := make(map[string]int, len(merged.GetColumns()))
	for idx, column := range merged.GetColumns() {
		positions[column.GetColumnName()] = idx
	}
	for _, column := range patch.GetColumns() {
		copied := proto.Clone(column).(*pb.ColumnValue)
		if idx, ok := positions[column.GetColumnName()]; ok {
			if isNullColumn(copied) && !isNullColumn(merged.Columns[idx]) {
				continue
			}
			merged.Columns[idx] = copied
			continue
		}
		positions[column.GetColumnName()] = len(merged.Columns)
		merged.Columns = append(merged.Columns, copied)
	}
	return merged
}

func isNullColumn(column *pb.ColumnValue) bool {
	return column == nil || column.GetValue() == nil
}

func buildInsertSQL(quotedTableName string, columns []*pb.ResultColumn) (string, error) {
	names := append([]string{}, resultBaseColumns...)
	for _, column := range columns {
		name := strings.TrimSpace(column.GetColumnName())
		if name == "" {
			continue
		}
		if _, err := quoteColumnName(name); err != nil {
			return "", err
		}
		names = append(names, name)
	}
	quotedNames := make([]string, 0, len(names))
	placeholders := make([]string, 0, len(names))
	for _, name := range names {
		quotedName, err := quoteColumnName(name)
		if err != nil {
			return "", err
		}
		quotedNames = append(quotedNames, quotedName)
		placeholders = append(placeholders, "?")
	}
	return fmt.Sprintf(`INSERT INTO %s (%s) VALUES (%s)`, quotedTableName, strings.Join(quotedNames, ","), strings.Join(placeholders, ",")), nil
}

func resultRowArgs(row *pb.TimeSeriesRow, columns []*pb.ResultColumn) ([]any, error) {
	normalized := normalizeTimeSeriesRow(row)
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(normalized)
	if err != nil {
		return nil, err
	}
	dimensionsRaw, err := json.Marshal(normalized.GetKey().GetDimensions())
	if err != nil {
		return nil, err
	}
	attributesRaw, err := json.Marshal(normalized.GetAttributes())
	if err != nil {
		return nil, err
	}
	values := make(map[string]*pb.ColumnValue, len(normalized.GetColumns()))
	for _, column := range normalized.GetColumns() {
		values[column.GetColumnName()] = column
	}
	key := normalized.GetKey()
	args := []any{
		timeSeriesRowKey(normalized),
		key.GetSpaceId(),
		key.GetDatasetId(),
		key.GetSubjectId(),
		key.GetFreq(),
		string(dimensionsRaw),
		key.GetDataTime(),
		string(attributesRaw),
		string(raw),
	}
	for _, column := range columns {
		args = append(args, sqlValue(values[column.GetColumnName()], column.GetValueType()))
	}
	return args, nil
}

func normalizeTimeSeriesRow(row *pb.TimeSeriesRow) *pb.TimeSeriesRow {
	if row == nil {
		return nil
	}
	out := proto.Clone(row).(*pb.TimeSeriesRow)
	if out.Key == nil {
		out.Key = &pb.TimeSeriesKey{}
	}
	if normalized, err := factkey.NormalizeTimeVersion(out.GetKey().GetDataTime()); err == nil {
		out.Key.DataTime = normalized
	}
	return out
}

func normalizeRowDataTime(row *pb.TimeSeriesRow) string {
	if row == nil || row.GetKey() == nil {
		return ""
	}
	if normalized, err := factkey.NormalizeTimeVersion(row.GetKey().GetDataTime()); err == nil {
		return normalized
	}
	return row.GetKey().GetDataTime()
}

func sqlValue(column *pb.ColumnValue, valueType pb.FieldValueType) any {
	if column == nil || column.GetValue() == nil {
		return nil
	}
	value := column.GetValue()
	switch valueType {
	case pb.FieldValueType_FIELD_VALUE_TYPE_INT:
		if _, ok := value.GetValue().(*pb.TypedValue_IntValue); ok {
			return value.GetIntValue()
		}
	case pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE:
		if _, ok := value.GetValue().(*pb.TypedValue_DoubleValue); ok {
			return value.GetDoubleValue()
		}
		if number, ok := factvalue.Numeric(value); ok {
			return number
		}
	case pb.FieldValueType_FIELD_VALUE_TYPE_BOOL:
		if _, ok := value.GetValue().(*pb.TypedValue_BoolValue); ok {
			return value.GetBoolValue()
		}
	case pb.FieldValueType_FIELD_VALUE_TYPE_TIME:
		if normalized, err := factkey.NormalizeTimeVersion(value.GetTimeValue()); err == nil {
			return normalized
		}
		return value.GetTimeValue()
	case pb.FieldValueType_FIELD_VALUE_TYPE_JSON:
		return value.GetJsonValue()
	case pb.FieldValueType_FIELD_VALUE_TYPE_BYTES:
		return value.GetBytesValue()
	case pb.FieldValueType_FIELD_VALUE_TYPE_STRING, pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED:
		return factvalue.String(value)
	}
	return factvalue.String(value)
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
	latestQuoted, err := quoteTableName(latestResultTableName(tableName))
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS %s`, latestQuoted)); err != nil {
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

type resultRowKey struct {
	rowKey   string
	dataTime string
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

func (s *ViewStore) fetchTimeSeriesRowsByResultKeys(ctx context.Context, quotedTableName string, selectColumns []string, columns []*pb.ResultColumn, keys []resultRowKey) ([]*pb.TimeSeriesRow, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	rowsByKey := make(map[string]*pb.TimeSeriesRow, len(keys))
	for start := 0; start < len(keys); start += rowKeyPredicateChunkSize {
		end := start + rowKeyPredicateChunkSize
		if end > len(keys) {
			end = len(keys)
		}
		where, args := resultRowKeyPredicate(keys[start:end])
		if where == "" {
			continue
		}
		query := fmt.Sprintf(`SELECT %s FROM %s WHERE %s`, strings.Join(selectColumns, ","), quotedTableName, where)
		rows, err := s.db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		scanned, err := scanResultRows(rows, columns)
		closeErr := rows.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		for _, row := range scanned {
			rowsByKey[rowPrimaryKey(row)] = row
		}
	}
	out := make([]*pb.TimeSeriesRow, 0, len(keys))
	for _, key := range keys {
		if row := rowsByKey[key.rowKey+"|"+key.dataTime]; row != nil {
			out = append(out, row)
		}
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

func normalizeDataTimeString(value string) string {
	if normalized, err := factkey.NormalizeTimeVersion(value); err == nil {
		return normalized
	}
	return value
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

const defaultTimeSeriesViewPageSize uint32 = 25

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

func timeSeriesKeyRowKey(datasetID string, subjectID string, freq string, dimensions map[string]string) string {
	return datasetID + "|" + factkey.BuildTimeSeriesDataKey(subjectID, freq, dimensions)
}

func timeSeriesKeyRowKeyPrefix(datasetID string, subjectID string, freq string) string {
	return datasetID + "|" + factkey.EscapePart(subjectID) + "|" + factkey.EscapePart(freq) + "|"
}

func escapeSQLLike(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
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

func (s *ViewStore) loadColumns(ctx context.Context, tableName string) ([]*pb.ResultColumn, error) {
	var raw string
	if err := s.db.QueryRowContext(ctx, `SELECT columns_json FROM moox_view_columns WHERE table_name = ?`, tableName).Scan(&raw); err != nil {
		return nil, err
	}
	rsp := &pb.QueryTimeSeriesRowsRsp{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(raw), rsp); err != nil {
		return nil, err
	}
	return rsp.GetColumns(), nil
}

func encodeColumns(columns []*pb.ViewColumn) (string, error) {
	out := make([]*pb.ResultColumn, 0, len(columns))
	for _, column := range columns {
		out = append(out, &pb.ResultColumn{
			ColumnName: column.GetColumnName(),
			OriginType: column.GetOriginType(),
			OriginId:   column.GetOriginId(),
			ValueType:  column.GetValueType(),
		})
	}
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(&pb.QueryTimeSeriesRowsRsp{Columns: out})
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func quoteTableName(tableName string) (string, error) {
	if !tableNamePattern.MatchString(tableName) {
		return "", fmt.Errorf("invalid duckdb table name %s", tableName)
	}
	return `"` + tableName + `"`, nil
}

func unquoteTableName(quotedTableName string) (string, error) {
	tableName := strings.Trim(quotedTableName, `"`)
	if !tableNamePattern.MatchString(tableName) {
		return "", fmt.Errorf("invalid duckdb table name %s", quotedTableName)
	}
	return tableName, nil
}

func quoteColumnName(columnName string) (string, error) {
	columnName = strings.TrimSpace(columnName)
	if columnName == "" || strings.Contains(columnName, `"`) || strings.ContainsAny(columnName, "\x00\r\n\t") {
		return "", fmt.Errorf("invalid duckdb column name %s", columnName)
	}
	return `"` + columnName + `"`, nil
}

func quoteIndexName(tableName string, suffix string) (string, error) {
	name := "idx_" + tableName + "_" + safeIndexNamePart(suffix)
	if !tableNamePattern.MatchString(name) {
		return "", fmt.Errorf("invalid duckdb index name %s", name)
	}
	return `"` + name + `"`, nil
}

func safeIndexNamePart(value string) string {
	value = unsafeIndexNameChar.ReplaceAllString(strings.TrimSpace(value), "_")
	value = strings.Trim(value, "_")
	if value == "" {
		return "column"
	}
	if first := value[0]; (first < 'A' || first > 'Z') && (first < 'a' || first > 'z') && first != '_' {
		value = "_" + value
	}
	return value
}

func duckDBType(valueType pb.FieldValueType) string {
	switch valueType {
	case pb.FieldValueType_FIELD_VALUE_TYPE_INT:
		return "BIGINT"
	case pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE:
		return "DOUBLE"
	case pb.FieldValueType_FIELD_VALUE_TYPE_BOOL:
		return "BOOLEAN"
	case pb.FieldValueType_FIELD_VALUE_TYPE_BYTES:
		return "BLOB"
	case pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
		pb.FieldValueType_FIELD_VALUE_TYPE_TIME,
		pb.FieldValueType_FIELD_VALUE_TYPE_JSON,
		pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED:
		return "VARCHAR"
	default:
		return "VARCHAR"
	}
}

func stringSet(values []string) map[string]bool {
	return factvalue.StringSet(values)
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
