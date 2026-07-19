//go:build legacy_viewindex && cgo

package duckdb

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"

	_ "github.com/marcboeker/go-duckdb/v2"
	"github.com/mooyang-code/moox/modules/storage/internal/rowkey"
	"github.com/mooyang-code/moox/modules/storage/internal/typedvalue"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// Options 保存 DuckDB 视图存储打开配置。
type Options struct {
	Path         string
	MaxOpenConns int
}

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

type resultColumnDef struct {
	name      string
	valueType pb.FieldValueType
}

func (s *ViewStore) lockResultTable(tableName string) func() {
	actual, _ := s.tableLocks.LoadOrStore(tableName, &sync.Mutex{})
	mu := actual.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

type persistedIndexMeta struct {
	viewVersion uint64
	entryCount  int64
	minVersion  string
	maxVersion  string
	schemaHash  string
	updatedAt   string
	indexedFrom string
	indexedTo   string
	checkpoints map[string]uint64
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

const rowKeyPredicateChunkSize = 200

func timeSeriesRowKey(row *pb.TimeSeriesRow) string {
	key := row.GetKey()
	return key.GetDatasetId() + "|" + rowkey.BuildTimeSeriesDataKey(key.GetSubjectId(), key.GetFreq(), key.GetDimensions())
}

type resultRowKey struct {
	rowKey   string
	dataTime string
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

func normalizeDataTimeString(value string) string {
	if normalized, err := rowkey.NormalizeTimeVersion(value); err == nil {
		return normalized
	}
	return value
}

const defaultTimeSeriesViewPageSize uint32 = 25

func timeSeriesKeyRowKey(datasetID string, subjectID string, freq string, dimensions map[string]string) string {
	return datasetID + "|" + rowkey.BuildTimeSeriesDataKey(subjectID, freq, dimensions)
}

func timeSeriesKeyRowKeyPrefix(datasetID string, subjectID string, freq string) string {
	return datasetID + "|" + rowkey.EscapePart(subjectID) + "|" + rowkey.EscapePart(freq) + "|"
}

func escapeSQLLike(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}

func stringSet(values []string) map[string]bool {
	return typedvalue.StringSet(values)
}
