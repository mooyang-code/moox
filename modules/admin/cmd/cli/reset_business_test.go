package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	adminschema "github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func seededAdminDB(t *testing.T) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data", "admin.db")
	require.NoError(t, ensureAdminSchema(path))
	db, err := gorm.Open(sqlite.Open(initSQLiteDSN(path)), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	for _, q := range []string{
		"INSERT INTO t_spaces (c_space_id, c_name) VALUES ('mooxsys', '系统'), ('team-a', '甲组')",
		"INSERT INTO t_space_members (c_space_id, c_user_id) VALUES ('mooxsys', 'u1'), ('team-a', 'u1')",
		"INSERT INTO t_activity_logs (c_source, c_category, c_title) VALUES ('x', 'y', 'z'), ('x', 'y', 'w')",
		"INSERT INTO t_collector_publish_leases (c_space_id) VALUES ('team-a')",
	} {
		_, err := sqlDB.Exec(q)
		require.NoError(t, err, q)
	}
	return path, sqlDB
}

func countRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM "+table).Scan(&n))
	return n
}

// 每张业务表都必须归类；schema 新增表而清单漏了会让这个测试失败。
func TestBusinessResetClassifiesEveryTable(t *testing.T) {
	_, db := seededAdminDB(t)
	require.NoError(t, checkBusinessTables(context.Background(), db))
	_, err := db.Exec("CREATE TABLE t_new_thing (c_id INTEGER)")
	require.NoError(t, err)
	require.ErrorContains(t, checkBusinessTables(context.Background(), db), "t_new_thing")
	_ = adminschema.AdminSQL
}

func TestBusinessResetScopes(t *testing.T) {
	path, db := seededAdminDB(t)
	var out bytes.Buffer
	require.Error(t, runResetBusinessCommand([]string{"reset-business", "--db-path", path}, &out, nil), "没有 --yes 不执行")
	require.NoError(t, runResetBusinessCommand([]string{"reset-business", "--db-path", path, "--scope", "all", "--dry-run"}, &out, nil))
	require.Equal(t, 2, countRows(t, db, "t_activity_logs"), "dry-run 不改动")

	out.Reset()
	require.NoError(t, runResetBusinessCommand([]string{"reset-business", "--db-path", path, "--yes"}, &out, nil))
	var res businessResetResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &res))
	require.Equal(t, "ok", res.Status)
	require.EqualValues(t, 2, res.Deleted["t_activity_logs"])
	require.Equal(t, 0, countRows(t, db, "t_activity_logs"))
	require.Equal(t, 0, countRows(t, db, "t_collector_publish_leases"))
	require.Equal(t, 2, countRows(t, db, "t_spaces"), "data 范围不动业务空间")
	require.FileExists(t, res.Backup)

	out.Reset()
	require.NoError(t, runResetBusinessCommand([]string{"reset-business", "--db-path", path, "--scope", "all", "--yes"}, &out, nil))
	require.Equal(t, 1, countRows(t, db, "t_spaces"), "默认空间保留")
	require.Equal(t, 1, countRows(t, db, "t_space_members"))
	var space string
	require.NoError(t, db.QueryRow("SELECT c_space_id FROM t_spaces").Scan(&space))
	require.Equal(t, "mooxsys", space)

	entries, err := os.ReadDir(filepath.Join(filepath.Dir(path), "reset-backups"))
	require.NoError(t, err)
	require.LessOrEqual(t, len(entries), 3)
}
