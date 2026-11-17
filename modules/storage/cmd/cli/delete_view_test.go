package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// newDeleteViewFixture 用真实的 metadata.sql 建库，放两个 View：目标 View 与一个旁证。
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
	for _, v := range []struct{ space, view string }{
		{"stockcn", "view_stockcn_kline_5m"},
		{"crypto", "view_factor_1h"},
	} {
		_, err = db.Exec(`INSERT INTO t_views (c_space_id, c_view_id, c_name, c_dataset_id) VALUES (?, ?, ?, 'ds')`, v.space, v.view, v.view)
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
	summary, err := deleteView(context.Background(), deleteViewOptions{metadataDB: path, view: "stockcn/view_stockcn_kline_5m"})
	require.NoError(t, err)
	require.Equal(t, 1, summary.DeletedViews)
	require.Equal(t, []retainedIndex{{Engine: "duckdb", IndexID: viewindex.ViewIndexID("stockcn", "view_stockcn_kline_5m")}}, summary.RetiredIndexes, "View 的索引留给 storage-view 启动时清理")

	require.Zero(t, countRows(t, path, `SELECT COUNT(*) FROM t_views WHERE c_view_id = 'view_stockcn_kline_5m'`))
	require.Equal(t, 1, countRows(t, path, `SELECT COUNT(*) FROM t_views WHERE c_view_id = 'view_factor_1h'`), "其他 View 不受影响")
}

func TestDeleteViewMissingViewChangesNothing(t *testing.T) {
	path := newDeleteViewFixture(t)
	_, err := deleteView(context.Background(), deleteViewOptions{metadataDB: path, view: "stockcn/view_not_there"})
	require.ErrorContains(t, err, "does not exist")
	require.Equal(t, 2, countRows(t, path, `SELECT COUNT(*) FROM t_views`))
}

func TestValidateDeleteViewOptions(t *testing.T) {
	ok := deleteViewOptions{metadataDB: "m.db", view: "mooxsys/view_a", yes: true}
	require.NoError(t, validateDeleteViewOptions(ok))

	noYes := ok
	noYes.yes = false
	require.ErrorContains(t, validateDeleteViewOptions(noYes), "--yes")

	for _, bad := range []string{"", "view_only", "/view", "space/", "a/b/c"} {
		invalid := ok
		invalid.view = bad
		require.ErrorContains(t, validateDeleteViewOptions(invalid), "--view", bad)
	}
}

// fakeStorageViewState 让 systemctl --user is-active 回答给定的状态，并记下收到的调用。
func fakeStorageViewState(t *testing.T, state string) *[]string {
	t.Helper()
	var calls []string
	previous := systemctlUser
	systemctlUser = func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		if args[0] == "is-active" && state != "active" {
			return []byte(state + "\n"), errors.New("exit status 3")
		}
		return []byte(state + "\n"), nil
	}
	t.Cleanup(func() { systemctlUser = previous })
	return &calls
}

func TestDeleteViewRefusesWhileStorageViewRuns(t *testing.T) {
	calls := fakeStorageViewState(t, "active")
	path := newDeleteViewFixture(t)
	err := runDeleteView([]string{"--metadata-db", path, "--view", "mooxsys/view_mooxsys_service_metrics", "--yes"}, os.Stdout, os.Stderr)
	require.ErrorContains(t, err, "must be stopped")
	require.Equal(t, []string{"is-active moox-storage-view.service"}, *calls)
	require.Equal(t, 2, countRows(t, path, `SELECT COUNT(*) FROM t_views`), "storage-view 在运行时不能动元数据")
}

// 停着（inactive、failed）才放行；正在启动、正在停止或状态说不清都拒绝。
func TestDeleteViewOnlyAcceptsStoppedStorageView(t *testing.T) {
	for _, state := range []string{"inactive", "failed"} {
		fakeStorageViewState(t, state)
		require.NoError(t, ensureStorageViewStopped(t.Context()), state)
	}
	for _, state := range []string{"activating", "deactivating"} {
		fakeStorageViewState(t, state)
		require.ErrorContains(t, ensureStorageViewStopped(t.Context()), "must be stopped", state)
	}
	for _, state := range []string{"", "unknown", "Failed to connect to bus"} {
		fakeStorageViewState(t, state)
		require.ErrorContains(t, ensureStorageViewStopped(t.Context()), "refusing", state)
	}
}
