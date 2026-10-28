package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	"github.com/stretchr/testify/require"
)

func TestOpenCreatesFactorSchema(t *testing.T) {
	s := openTestStore(t)
	var tables []string
	require.NoError(t, s.db.Raw(`
		SELECT name FROM sqlite_master WHERE type = 'table' AND name LIKE 't_factor_%' ORDER BY name
	`).Scan(&tables).Error)
	require.Equal(t, []string{"t_factor_categories", "t_factor_defs", "t_factor_engine_heartbeat", "t_factor_recalc_job_bindings", "t_factor_recalc_jobs", "t_factor_task_members", "t_factor_tasks", "t_factor_udf_versions", "t_factor_udfs"}, tables)
}

// 已有库缺少后来新增的引擎心跳表时，启动时补建，已有数据保留。
func TestApplySchemaAddsMissingHeartbeatTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "factor.db")
	s, err := Open(&Options{Path: path})
	require.NoError(t, err)
	require.NoError(t, s.db.Exec("DROP TABLE t_factor_engine_heartbeat").Error)
	require.NoError(t, s.Close())
	s, err = Open(&Options{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	_, ok, err := s.LastEngineHeartbeat(context.Background())
	require.NoError(t, err)
	require.False(t, ok)
}

// 已有库（没有自定义算子相关的三张表）启动时补建新表，已有的表与数据不变：新表不改动任何已有表，所以现有的 ApplySchema
// 严格校验不会让新版本在现有库上启动失败。
func TestApplySchemaAddsUdfTablesToAnExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "factor.db")
	s, err := Open(&Options{Path: path})
	require.NoError(t, err)
	require.NoError(t, s.CreateFactor(context.Background(), domain.FactorDef{FactorID: "f", Name: "n", Spec: "x", SpecHash: "h"}))
	for _, table := range []string{"t_factor_recalc_job_bindings", "t_factor_udf_versions", "t_factor_udfs"} {
		require.NoError(t, s.db.Exec("DROP TABLE "+table).Error)
	}
	require.NoError(t, s.Close())
	s, err = Open(&Options{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	var n int
	require.NoError(t, s.db.Raw("SELECT COUNT(*) FROM t_factor_udfs").Scan(&n).Error)
	_, err = s.GetFactor(context.Background(), "f")
	require.NoError(t, err, "已有数据保留")
}

func TestApplySchemaRejectsNonCurrentTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "factor.db")
	s, err := Open(&Options{Path: path})
	require.NoError(t, err)
	require.NoError(t, s.db.Exec("CREATE TABLE t_factor_old_ledger (c_id TEXT)").Error)
	require.ErrorContains(t, s.ApplySchema("CREATE TABLE IF NOT EXISTS t_factor_tasks(c_id TEXT)"), "unexpected table t_factor_old_ledger")
	require.NoError(t, s.Close())
}

func TestBuildSQLiteDSNUsesForeignKeysAndDurablePragmas(t *testing.T) {
	dsn := buildSQLiteDSN("./data/factor/factor.db")
	for _, want := range []string{
		"_pragma=journal_mode(WAL)", "_pragma=foreign_keys(ON)",
		"_pragma=synchronous(NORMAL)", "_pragma=busy_timeout(5000)",
		"_pragma=wal_autocheckpoint(1000)",
	} {
		require.True(t, strings.Contains(dsn, want), "dsn %q missing %q", dsn, want)
	}
	require.NotContains(t, dsn, "synchronous(OFF)")
}

func TestFactorMtimeTriggersUpdateOnDirectSQL(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateTask(ctx, testTask("task_prices", "disabled")))
	require.NoError(t, s.CreateFactor(ctx, testFactorDef("factor_close")))
	_, err := s.AddMember(ctx, "task_prices", "factor_close", "factor_close", domain.MemberKindFixed, nil)
	require.NoError(t, err)
	_, err = s.CreateRecalcJob(ctx, RecalcJob{
		JobID: "job-1", RequestID: "req-1", TaskID: "task_prices", StartTime: 100, EndTime: 200,
	})
	require.NoError(t, err)

	for _, tableAndKey := range []struct{ table, key string }{
		{"t_factor_tasks", "task_prices"},
		{"t_factor_defs", "factor_close"},
		{"t_factor_task_members", "factor_close"},
		{"t_factor_recalc_jobs", "job-1"},
	} {
		keyColumn := map[string]string{
			"t_factor_tasks": "c_task_id", "t_factor_defs": "c_factor_id", "t_factor_recalc_jobs": "c_job_id",
			"t_factor_task_members": "c_factor_id",
		}[tableAndKey.table]
		touched := "c_status = c_status"
		if tableAndKey.table == "t_factor_defs" {
			touched = "c_name = c_name"
		}
		require.NoError(t, s.db.Exec("UPDATE "+tableAndKey.table+" SET c_mtime = '2000-01-01 00:00:00' WHERE "+keyColumn+" = ?", tableAndKey.key).Error)
		require.NoError(t, s.db.Exec("UPDATE "+tableAndKey.table+" SET "+touched+" WHERE "+keyColumn+" = ?", tableAndKey.key).Error)
		var updated string
		require.NoError(t, s.db.Raw("SELECT c_mtime FROM "+tableAndKey.table+" WHERE "+keyColumn+" = ?", tableAndKey.key).Scan(&updated).Error)
		require.NotEqual(t, "2000-01-01 00:00:00", updated)
	}
}

func TestApplySchemaRejectsObsoleteDefsColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "factor.db")
	s, err := Open(&Options{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	require.NoError(t, s.db.Exec("DROP TABLE t_factor_task_members").Error)
	require.NoError(t, s.db.Exec("DROP TABLE t_factor_defs").Error)
	require.NoError(t, s.db.Exec(`CREATE TABLE t_factor_defs (
		c_factor_id TEXT NOT NULL PRIMARY KEY, c_task_id TEXT NOT NULL, c_name TEXT NOT NULL,
		c_factor_type TEXT NOT NULL, c_obsolete TEXT NOT NULL,
		c_input_columns_json TEXT NOT NULL, c_outputs_json TEXT NOT NULL, c_params_json TEXT NOT NULL,
		c_lookback_periods INTEGER NOT NULL, c_allow_partial_universe INTEGER NOT NULL,
		c_status TEXT NOT NULL, c_ctime DATETIME, c_mtime DATETIME)`).Error)
	require.ErrorContains(t, s.ApplySchema(factorschema.AllSQL()), "fresh database")
}
