package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	"github.com/stretchr/testify/require"
)

func TestOpenCreatesFactorSchema(t *testing.T) {
	s := openTestStore(t)
	var tables []string
	require.NoError(t, s.db.Raw(`
		SELECT name FROM sqlite_master WHERE type = 'table' AND name LIKE 't_factor_%' ORDER BY name
	`).Scan(&tables).Error)
	require.Equal(t, []string{"t_factor_defs", "t_factor_recalc_jobs", "t_factor_set_members", "t_factor_sets"}, tables)
}

func TestApplySchemaRejectsNonCurrentTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "factor.db")
	s, err := Open(&Options{Path: path})
	require.NoError(t, err)
	require.NoError(t, s.db.Exec("CREATE TABLE t_factor_old_ledger (c_id TEXT)").Error)
	require.ErrorContains(t, s.ApplySchema("CREATE TABLE IF NOT EXISTS t_factor_sets(c_id TEXT)"), "only factor sets")
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
	require.NoError(t, s.CreateSet(ctx, testSet("set_prices", "disabled")))
	require.NoError(t, s.CreateFactor(ctx, testFactorDef("factor_close")))
	_, err := s.AddMember(ctx, "set_prices", "factor_close")
	require.NoError(t, err)
	_, err = s.CreateRecalcJob(ctx, RecalcJob{
		JobID: "job-1", RequestID: "req-1", SetID: "set_prices", StartTime: 100, EndTime: 200,
	})
	require.NoError(t, err)

	for _, tableAndKey := range []struct{ table, key string }{
		{"t_factor_sets", "set_prices"},
		{"t_factor_defs", "factor_close"},
		{"t_factor_set_members", "factor_close"},
		{"t_factor_recalc_jobs", "job-1"},
	} {
		keyColumn := map[string]string{
			"t_factor_sets": "c_set_id", "t_factor_defs": "c_factor_id", "t_factor_recalc_jobs": "c_job_id",
			"t_factor_set_members": "c_factor_id",
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
	require.NoError(t, s.db.Exec("DROP TABLE t_factor_set_members").Error)
	require.NoError(t, s.db.Exec("DROP TABLE t_factor_defs").Error)
	require.NoError(t, s.db.Exec(`CREATE TABLE t_factor_defs (
		c_factor_id TEXT NOT NULL PRIMARY KEY, c_set_id TEXT NOT NULL, c_name TEXT NOT NULL,
		c_factor_type TEXT NOT NULL, c_source_code TEXT NOT NULL, c_source_hash TEXT NOT NULL,
		c_input_columns_json TEXT NOT NULL, c_outputs_json TEXT NOT NULL, c_params_json TEXT NOT NULL,
		c_lookback_periods INTEGER NOT NULL, c_allow_partial_universe INTEGER NOT NULL,
		c_status TEXT NOT NULL, c_ctime DATETIME, c_mtime DATETIME)`).Error)
	require.ErrorContains(t, s.ApplySchema(factorschema.AllSQL()), "fresh database")
}
