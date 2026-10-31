package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// newDeleteViewFixture 用真实的 metadata.sql 建库，放两个 View：目标 View 带列与构建中的索引，另一个作为旁证。
// 夹具写入时不开外键，避免为了插 t_views 而铺满数据集、数据源等父表；工具自己会打开外键并依赖级联删除。
func newDeleteViewFixture(t *testing.T) string {
	t.Helper()
	ddl, err := os.ReadFile(filepath.Join("..", "..", "schema", "metadata.sql"))
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "storage_metadata.db")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close()
	db.SetMaxOpenConns(1) // PRAGMA 只作用于当前连接
	_, err = db.Exec(string(ddl))
	require.NoError(t, err)
	_, err = db.Exec(`PRAGMA foreign_keys = OFF`)
	require.NoError(t, err)
	for _, v := range []struct{ space, view, active string }{
		{"mooxsys", "view_mooxsys_service_metrics", "idx-service-b"},
		{"crypto", "view_factor_1h", "idx-factor"},
	} {
		_, err = db.Exec(`INSERT INTO t_views (c_space_id, c_view_id, c_name, c_dataset_id, c_active_index_id) VALUES (?, ?, ?, 'ds', ?)`, v.space, v.view, v.view, v.active)
		require.NoError(t, err)
		_, err = db.Exec(`INSERT INTO t_view_columns (c_space_id, c_view_id, c_column_name, c_origin_type, c_origin_id, c_value_type) VALUES (?, ?, 'value', 'dataset_column', 'value', 'double')`, v.space, v.view)
		require.NoError(t, err)
	}
	return path
}

func countRows(t *testing.T, path, query string, args ...any) int {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close()
	var n int
	require.NoError(t, db.QueryRow(query, args...).Scan(&n))
	return n
}

func TestDeleteViewRemovesOnlyTheNamedView(t *testing.T) {
	path := newDeleteViewFixture(t)
	summary, err := deleteView(context.Background(), deleteViewOptions{metadataDB: path, view: "mooxsys/view_mooxsys_service_metrics"})
	require.NoError(t, err)
	require.Equal(t, 1, summary.DeletedViews)
	require.Equal(t, []retainedIndex{{Engine: "duckdb", IndexID: "idx-service-b"}}, summary.RetiredIndexes, "活动索引要交给清理定时器")

	require.Zero(t, countRows(t, path, `SELECT COUNT(*) FROM t_views WHERE c_view_id = 'view_mooxsys_service_metrics'`))
	require.Zero(t, countRows(t, path, `SELECT COUNT(*) FROM t_view_columns WHERE c_view_id = 'view_mooxsys_service_metrics'`), "列随 View 级联删除")
	require.Equal(t, 1, countRows(t, path, `SELECT COUNT(*) FROM t_views WHERE c_view_id = 'view_factor_1h'`), "其他 View 不受影响")
	require.Equal(t, 1, countRows(t, path, `SELECT COUNT(*) FROM t_view_columns WHERE c_view_id = 'view_factor_1h'`))
}

func TestDeleteViewMissingViewChangesNothing(t *testing.T) {
	path := newDeleteViewFixture(t)
	_, err := deleteView(context.Background(), deleteViewOptions{metadataDB: path, view: "mooxsys/view_not_there"})
	require.ErrorContains(t, err, "does not exist")
	require.Equal(t, 2, countRows(t, path, `SELECT COUNT(*) FROM t_views`))
}

func TestValidateDeleteViewOptions(t *testing.T) {
	ok := deleteViewOptions{metadataDB: "m.db", packageRoot: "/tmp/storage", view: "mooxsys/view_a", yes: true}
	require.NoError(t, validateDeleteViewOptions(ok))

	noYes := ok
	noYes.yes = false
	require.ErrorContains(t, validateDeleteViewOptions(noYes), "--yes")

	noRoot := ok
	noRoot.packageRoot = " "
	require.ErrorContains(t, validateDeleteViewOptions(noRoot), "--package-root")

	for _, bad := range []string{"", "view_only", "/view", "space/", "a/b/c"} {
		invalid := ok
		invalid.view = bad
		require.ErrorContains(t, validateDeleteViewOptions(invalid), "--view", bad)
	}
}

func TestDeleteViewRefusesWhileStorageViewRuns(t *testing.T) {
	root := t.TempDir()
	script := "#!/bin/sh\necho '{\"components\":[{\"id\":\"storage-view\",\"state\":\"running\"}]}'\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "status.sh"), []byte(script), 0o755))
	path := newDeleteViewFixture(t)
	err := runDeleteView([]string{"--metadata-db", path, "--package-root", root, "--view", "mooxsys/view_mooxsys_service_metrics", "--yes"}, os.Stdout, os.Stderr)
	require.ErrorContains(t, err, "must be stopped")
	require.Equal(t, 2, countRows(t, path, `SELECT COUNT(*) FROM t_views`), "storage-view 在运行时不能动元数据")
}

func TestDeleteViewAcceptsPausedStorageView(t *testing.T) {
	for _, state := range []string{"stopped", "paused"} {
		root := t.TempDir()
		script := "#!/bin/sh\necho '{\"components\":[{\"id\":\"storage-view\",\"state\":\"" + state + "\"}]}'\n"
		require.NoError(t, os.WriteFile(filepath.Join(root, "status.sh"), []byte(script), 0o755))
		require.NoError(t, ensureStorageViewStopped(root), state)
	}
	root := t.TempDir()
	script := "#!/bin/sh\necho '{\"components\":[{\"id\":\"storage-view\",\"state\":\"starting\"}]}'\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "status.sh"), []byte(script), 0o755))
	require.ErrorContains(t, ensureStorageViewStopped(root), "refusing")
}
