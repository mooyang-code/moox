//go:build e2e

package e2e

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"regexp"
	"testing"

	blevelib "github.com/blevesearch/bleve/v2"
	cpebble "github.com/cockroachdb/pebble"
	_ "github.com/marcboeker/go-duckdb/v2"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"
)

type storagePaths struct {
	metadata string
	pebble   string
	duckdb   string
	bleve    string
}

func testDirectStorageCounts(ctx context.Context, t *testing.T) {
	t.Helper()
	expectedKlines := len(klines(t))

	require.NoError(t, harness.StopProcess(), "直读底层存储前应先停止服务进程释放文件锁")
	paths := e2eStoragePaths(harness)

	activeResult := verifySQLiteMetadataCounts(ctx, t, paths.metadata, expectedKlines)
	verifyPebbleRows(t, paths.pebble, expectedKlines)
	verifyDuckDBViewRows(ctx, t, paths.duckdb, activeResult, expectedKlines)
	verifyBleveIndexRows(t, paths.bleve, expectedKlines)
}

func e2eStoragePaths(h *Harness) storagePaths {
	root := filepath.Join(h.workDir, "var", "storage")
	return storagePaths{
		metadata: filepath.Join(root, "metadata", "metadata.db"),
		pebble:   filepath.Join(root, "pebble", "main"),
		duckdb:   filepath.Join(root, "duckdb", "views.duckdb"),
		bleve:    filepath.Join(root, "bleve", "default"),
	}
}

func verifySQLiteMetadataCounts(ctx context.Context, t *testing.T, path string, expectedKlines int) string {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	require.NoError(t, db.PingContext(ctx))

	requireSQLCount(t, ctx, db, 1, `SELECT COUNT(*) FROM t_spaces WHERE c_space_id = ?`, e2eSpaceID)
	requireSQLCount(t, ctx, db, 1, `SELECT COUNT(*) FROM t_data_sources WHERE c_space_id = ? AND c_data_source_id = ?`, e2eSpaceID, dataSourceID)
	requireSQLCount(t, ctx, db, 1, `SELECT COUNT(*) FROM t_subjects WHERE c_space_id = ? AND c_subject_id = ?`, e2eSpaceID, subjectID)
	requireSQLCount(t, ctx, db, 1, `SELECT COUNT(*) FROM t_subjects WHERE c_space_id = ? AND c_subject_id = ?`, e2eSpaceID, cliSubjectID)
	requireSQLCount(t, ctx, db, 1, `SELECT COUNT(*) FROM t_subject_symbols WHERE c_space_id = ? AND c_subject_id = ?`, e2eSpaceID, subjectID)
	requireSQLCount(t, ctx, db, 3, `SELECT COUNT(*) FROM t_datasets WHERE c_space_id = ?`, e2eSpaceID)
	requireSQLCount(t, ctx, db, 2, `SELECT COUNT(*) FROM t_dataset_subjects WHERE c_space_id = ?`, e2eSpaceID)
	requireSQLCount(t, ctx, db, 0, `SELECT COUNT(*) FROM t_dataset_subjects WHERE c_space_id = ? AND c_dataset_id = ? AND c_subject_role = 'object'`, e2eSpaceID, symbolsDatasetID)
	requireSQLCount(t, ctx, db, 11, `SELECT COUNT(*) FROM t_fields WHERE c_space_id = ?`, e2eSpaceID)
	requireSQLCount(t, ctx, db, 13, `SELECT COUNT(*) FROM t_dataset_columns WHERE c_space_id = ?`, e2eSpaceID)
	requireSQLCount(t, ctx, db, 2, `SELECT COUNT(*) FROM t_storage_nodes`)
	requireSQLCount(t, ctx, db, 2, `SELECT COUNT(*) FROM t_storage_devices`)
	requireSQLCount(t, ctx, db, 3, `SELECT COUNT(*) FROM t_storage_routes WHERE c_space_id = ?`, e2eSpaceID)
	requireSQLCount(t, ctx, db, 1, `SELECT COUNT(*) FROM t_views WHERE c_space_id = ? AND c_view_id = ?`, e2eSpaceID, viewID)
	requireSQLCount(t, ctx, db, 1, `SELECT COUNT(*) FROM t_view_columns WHERE c_space_id = ? AND c_view_id = ?`, e2eSpaceID, viewID)
	requireSQLCountAtLeast(t, ctx, db, 1, `SELECT COUNT(*) FROM t_archive_files WHERE c_space_id = ? AND c_dataset_id = ? AND c_file_format = 'parquet' AND c_row_count > 0`, e2eSpaceID, datasetID)

	archiveRows := querySQLCount(t, ctx, db, `SELECT COALESCE(SUM(c_row_count), 0) FROM t_archive_files WHERE c_space_id = ? AND c_dataset_id = ?`, e2eSpaceID, datasetID)
	require.GreaterOrEqual(t, archiveRows, int64(expectedKlines*len(klineColumns())), "SQLite archive metadata row_count 应覆盖主 K 线数据集的列式归档行数")

	var activeResult, buildStatus string
	err = db.QueryRowContext(ctx, `
		SELECT c_active_result, c_build_status
		FROM t_views
		WHERE c_space_id = ? AND c_view_id = ?
	`, e2eSpaceID, viewID).Scan(&activeResult, &buildStatus)
	require.NoError(t, err)
	require.Equal(t, "active", buildStatus)
	require.NotEmpty(t, activeResult, "View 应在 SQLite 元数据中记录 active_result")
	return activeResult
}

func verifyPebbleRows(t *testing.T, path string, expectedKlines int) {
	t.Helper()
	db, err := cpebble.Open(path, &cpebble.Options{ReadOnly: true})
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()

	iter, err := db.NewIter(&cpebble.IterOptions{})
	require.NoError(t, err)
	defer func() { require.NoError(t, iter.Close()) }()

	counts := make(map[string]int)
	for valid := iter.First(); valid; valid = iter.Next() {
		row := &pb.DataRow{}
		require.NoError(t, proto.Unmarshal(iter.Value(), row))
		counts[row.GetKey().GetScope().GetDatasetId()]++
	}
	require.NoError(t, iter.Error())
	require.Equal(t, expectedKlines, counts[datasetID], "Pebble 主存应保存全部 K 线事实行")
	require.Equal(t, 1, counts[cliDatasetID], "Pebble 主存应保存 CLI 导入的一行 K 线")
	require.Equal(t, 2, counts[symbolsDatasetID], "Pebble 主存应保存对象型数据集两行")
	require.Equal(t, expectedKlines+3, sumCounts(counts), "Pebble 不应出现额外事实行")
}

func verifyDuckDBViewRows(ctx context.Context, t *testing.T, path string, tableName string, expectedKlines int) {
	t.Helper()
	quoted, err := quoteDuckDBIdentifier(tableName)
	require.NoError(t, err)

	db, err := sql.Open("duckdb", path)
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	require.NoError(t, db.PingContext(ctx))

	requireSQLCount(t, ctx, db, 1, `SELECT COUNT(*) FROM moox_view_columns WHERE table_name = ?`, tableName)
	got := querySQLCount(t, ctx, db, fmt.Sprintf(`SELECT COUNT(*) FROM %s`, quoted))
	require.Equal(t, int64(expectedKlines), got, "DuckDB active view 结果表应与 K 线事实行数一致")
}

func verifyBleveIndexRows(t *testing.T, path string, expectedKlines int) {
	t.Helper()
	index, err := blevelib.Open(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, index.Close()) }()

	docCount, err := index.DocCount()
	require.NoError(t, err)
	require.Equal(t, uint64(expectedKlines), docCount, "Bleve 应只索引带 text_indexed 列的 K 线事实行")

	spaceQuery := blevelib.NewTermQuery(e2eSpaceID)
	spaceQuery.SetField("space_id")
	datasetQuery := blevelib.NewTermQuery(datasetID)
	datasetQuery.SetField("dataset_id")
	textQuery := blevelib.NewMatchQuery("kline")
	query := blevelib.NewConjunctionQuery(spaceQuery, datasetQuery, textQuery)
	result, err := index.Search(blevelib.NewSearchRequestOptions(query, 1, 0, false))
	require.NoError(t, err)
	require.Equal(t, uint64(expectedKlines), result.Total, "Bleve 直接检索 kline 应命中全部 K 线文档")
}

func requireSQLCount(t *testing.T, ctx context.Context, db *sql.DB, want int64, query string, args ...any) {
	t.Helper()
	require.Equal(t, want, querySQLCount(t, ctx, db, query, args...))
}

func requireSQLCountAtLeast(t *testing.T, ctx context.Context, db *sql.DB, wantMin int64, query string, args ...any) {
	t.Helper()
	require.GreaterOrEqual(t, querySQLCount(t, ctx, db, query, args...), wantMin)
}

func querySQLCount(t *testing.T, ctx context.Context, db *sql.DB, query string, args ...any) int64 {
	t.Helper()
	var got int64
	require.NoError(t, db.QueryRowContext(ctx, query, args...).Scan(&got))
	return got
}

func sumCounts(counts map[string]int) int {
	var total int
	for _, count := range counts {
		total += count
	}
	return total
}

var duckDBIdentifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func quoteDuckDBIdentifier(value string) (string, error) {
	if !duckDBIdentifierPattern.MatchString(value) {
		return "", fmt.Errorf("invalid duckdb identifier %q", value)
	}
	return `"` + value + `"`, nil
}
