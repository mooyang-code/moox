package bootstrap

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/stretchr/testify/require"
)

func newResetEnv(t *testing.T) (*lifecycle.Controller, *store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "strategy.sqlite")
	db, err := store.Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	life, err := newLifecycle(dbPath, lifecycleDeps{db: db})
	require.NoError(t, err)
	life.Refresh(context.Background())
	return life, db, dbPath
}

func seedStrategyRows(t *testing.T, db *store.Store) {
	t.Helper()
	sqlDB, err := db.SQLDB()
	require.NoError(t, err)
	_, err = sqlDB.Exec(`INSERT INTO t_strategy_defs (c_strategy_id, c_name, c_dsl_yaml, c_dsl_hash, c_ctime, c_mtime)
		VALUES ('s1', 'demo', 'x', 'h1', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)
	require.NoError(t, err)
}

func rowCount(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM "+table).Scan(&n))
	return n
}

// schema 里的每张业务表都必须在重置清单里归类：新增表忘了归类，这个测试失败（运行时 Plan 也会拒绝）。
func TestStrategyResetClassifiesEveryTable(t *testing.T) {
	_, db, _ := newResetEnv(t)
	sqlDB, err := db.SQLDB()
	require.NoError(t, err)
	actual, err := lifecycle.BusinessTables(context.Background(), sqlDB)
	require.NoError(t, err)
	unclassified, missing := lifecycle.ClassifyTables(actual, strategyDataTables, strategyConfigTables)
	require.Empty(t, unclassified, "有表没有归类到重置清单")
	require.Empty(t, missing, "重置清单里有不存在的表")
}

// 端到端：暂停 → plan → apply → 完成。data 只清运行数据；备份是完整可用的库；all 连配置一起清；表结构保留。
func TestStrategyResetEndToEnd(t *testing.T) {
	life, db, dbPath := newResetEnv(t)
	ctx := context.Background()
	sqlDB, err := db.SQLDB()
	require.NoError(t, err)
	seedStrategyRows(t, db)
	require.Equal(t, 1, rowCount(t, sqlDB, "t_strategy_defs"))

	_, err = life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdPause, PauseKind: lifecycle.PauseSystem})
	require.NoError(t, err)

	plan, err := life.ResetPlan(ctx, lifecycle.ResetData)
	require.NoError(t, err)
	require.Empty(t, plan.Blockers)
	require.Len(t, plan.Steps, 2)
	require.Contains(t, plan.Keeps, "策略定义、实例、会话（配置）")
	_, err = life.ResetApply(ctx, lifecycle.ResetApplyRequest{Token: plan.Token, Scope: lifecycle.ResetData, By: "ops"})
	require.NoError(t, err)
	require.NoError(t, life.ResetWait(ctx))
	require.Equal(t, 1, rowCount(t, sqlDB, "t_strategy_defs"), "data 范围保留策略定义")

	backups, err := filepath.Glob(filepath.Join(filepath.Dir(dbPath), "reset-backups", "*", "strategy.sqlite"))
	require.NoError(t, err)
	require.Len(t, backups, 1)
	backup, err := sql.Open("sqlite", backups[0])
	require.NoError(t, err)
	defer backup.Close()
	require.Equal(t, 1, rowCount(t, backup, "t_strategy_defs"), "备份是完整可用的库")

	// 重置完成后模块是未初始化 + 等待 init；要再重置先 init 再暂停。
	require.Equal(t, lifecycle.StageUninitialized, life.Stage(lifecycle.Ref{}))
	_, err = life.Command(ctx, lifecycle.CmdInit, lifecycle.Ref{})
	require.NoError(t, err)
	_, err = life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdPause, PauseKind: lifecycle.PauseSystem})
	require.NoError(t, err)
	plan, err = life.ResetPlan(ctx, lifecycle.ResetAll)
	require.NoError(t, err)
	_, err = life.ResetApply(ctx, lifecycle.ResetApplyRequest{Token: plan.Token, Scope: lifecycle.ResetAll})
	require.NoError(t, err)
	require.NoError(t, life.ResetWait(ctx))
	require.Zero(t, rowCount(t, sqlDB, "t_strategy_defs"), "all 范围清掉配置")
	tables, err := lifecycle.BusinessTables(ctx, sqlDB)
	require.NoError(t, err)
	require.Len(t, tables, len(strategyDataTables)+len(strategyConfigTables), "表结构保留")
}
