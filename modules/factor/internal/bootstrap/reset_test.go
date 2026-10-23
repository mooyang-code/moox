package bootstrap

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/stretchr/testify/require"
)

func newMgrResetEnv(t *testing.T) (*lifecycle.Controller, *store.Store) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "factor.db")
	db, err := store.Open(&store.Options{Path: dbPath})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	life, err := newMgrLifecycle(db, dbPath)
	require.NoError(t, err)
	life.Refresh(context.Background())
	return life, db
}

func TestMgrResetClassifiesEveryTable(t *testing.T) {
	_, db := newMgrResetEnv(t)
	sqlDB, err := db.SQLDB()
	require.NoError(t, err)
	actual, err := lifecycle.BusinessTables(context.Background(), sqlDB)
	require.NoError(t, err)
	unclassified, missing := lifecycle.ClassifyTables(actual, mgrDataTables, mgrConfigTables)
	require.Empty(t, unclassified, "有表没有归类到重置清单")
	require.Empty(t, missing, "重置清单里有不存在的表")
}

// 暂停时修改类接口被拒绝（门禁）；重置 all 清掉配置和运行数据，表结构保留，完成后需要 init。
func TestMgrResetAllAndMutationGate(t *testing.T) {
	life, db := newMgrResetEnv(t)
	ctx := context.Background()
	gate := mutationGate(life)
	require.NoError(t, gate(), "正常时放行")

	_, err := life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdPause, PauseKind: lifecycle.PauseSystem})
	require.NoError(t, err)
	require.Error(t, gate(), "暂停时拒绝修改")

	sqlDB, err := db.SQLDB()
	require.NoError(t, err)
	_, err = sqlDB.Exec(`INSERT INTO t_factor_defs (c_factor_id, c_name, c_spec, c_spec_hash) VALUES ('f', 'n', 'x', 'h')`)
	require.NoError(t, err)

	plan, err := life.ResetPlan(ctx, lifecycle.ResetAll)
	require.NoError(t, err)
	require.Empty(t, plan.Blockers)
	require.Contains(t, plan.Notes[0], "setup factors")
	_, err = life.ResetApply(ctx, lifecycle.ResetApplyRequest{Token: plan.Token, Scope: lifecycle.ResetAll, By: "ops"})
	require.NoError(t, err)
	require.NoError(t, life.ResetWait(ctx))
	require.Equal(t, "done", life.ResetStatus().Operation.State)
	require.Error(t, gate(), "重置完成、init 之前仍然拒绝修改")
	var backups []string
	backups, err = filepath.Glob(filepath.Join(filepath.Dir(sqlDBPath(t, sqlDB)), "reset-backups", "*", "factor.db"))
	require.NoError(t, err)
	require.Len(t, backups, 1, "重置前备份了数据库")
	for _, table := range append(append([]string{}, mgrDataTables...), mgrConfigTables...) {
		var n int
		require.NoError(t, sqlDB.QueryRow("SELECT COUNT(*) FROM "+table).Scan(&n))
		if table == "t_factor_categories" {
			require.Equal(t, 1, n, "只剩补回的默认分类")
			continue
		}
		require.Zero(t, n, table)
	}
	_, err = life.Command(ctx, lifecycle.CmdInit, lifecycle.Ref{})
	require.NoError(t, err)
	require.NoError(t, gate(), "init 后放行")
}

// sqlDBPath 返回数据库文件路径（SQLite 的 PRAGMA database_list）。
func sqlDBPath(t *testing.T, db *sql.DB) string {
	t.Helper()
	var seq int
	var name, file string
	require.NoError(t, db.QueryRow("PRAGMA database_list").Scan(&seq, &name, &file))
	return file
}
