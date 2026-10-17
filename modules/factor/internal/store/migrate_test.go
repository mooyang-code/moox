package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	_ "github.com/glebarez/go-sqlite"
)

// legacySchema 是 Python 时代的表结构（迁移前的生产数据库）。
const legacySchema = `
PRAGMA foreign_keys = ON;
CREATE TABLE t_factor_sets (
    c_set_id TEXT NOT NULL PRIMARY KEY, c_space_id TEXT NOT NULL, c_source_dataset_id TEXT NOT NULL,
    c_freq TEXT NOT NULL, c_subject_mode TEXT NOT NULL DEFAULT 'all', c_subjects_json TEXT NOT NULL DEFAULT '[]',
    c_result_dataset_id TEXT NOT NULL, c_status TEXT NOT NULL DEFAULT 'pending',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_subject_mode IN ('all', 'include')), CHECK (c_status IN ('pending', 'enabled', 'disabled', 'deleting')),
    UNIQUE (c_space_id, c_source_dataset_id, c_freq), UNIQUE (c_result_dataset_id)
);
CREATE TABLE t_factor_defs (
    c_factor_id TEXT NOT NULL PRIMARY KEY, c_name TEXT NOT NULL, c_factor_type TEXT NOT NULL,
    c_source_code TEXT NOT NULL, c_source_hash TEXT NOT NULL, c_input_columns_json TEXT NOT NULL,
    c_outputs_json TEXT NOT NULL, c_params_json TEXT NOT NULL DEFAULT '{}', c_lookback_periods INTEGER NOT NULL,
    c_allow_partial_universe INTEGER NOT NULL DEFAULT 0,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE t_factor_set_members (
    c_set_id TEXT NOT NULL, c_factor_id TEXT NOT NULL, c_status TEXT NOT NULL DEFAULT 'disabled',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_set_id, c_factor_id), CHECK (c_status IN ('enabled', 'disabled')),
    FOREIGN KEY (c_set_id) REFERENCES t_factor_sets (c_set_id),
    FOREIGN KEY (c_factor_id) REFERENCES t_factor_defs (c_factor_id) ON DELETE RESTRICT
);
CREATE TABLE t_factor_recalc_jobs (
    c_job_id TEXT NOT NULL PRIMARY KEY, c_request_id TEXT NOT NULL, c_set_id TEXT NOT NULL,
    c_factor_ids_json TEXT NOT NULL DEFAULT '[]', c_subjects_json TEXT NOT NULL DEFAULT '[]',
    c_factors_omitted INTEGER NOT NULL DEFAULT 0, c_subjects_omitted INTEGER NOT NULL DEFAULT 0,
    c_start_time INTEGER NOT NULL, c_end_time INTEGER NOT NULL, c_status TEXT NOT NULL DEFAULT 'accepted',
    c_progress_time INTEGER NOT NULL DEFAULT 0, c_error TEXT NOT NULL DEFAULT '',
    c_engine_id TEXT NOT NULL DEFAULT '', c_lease_token TEXT NOT NULL DEFAULT '', c_lease_expires_at INTEGER NOT NULL DEFAULT 0,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (c_request_id)
);
INSERT INTO t_factor_sets (c_set_id, c_space_id, c_source_dataset_id, c_freq, c_result_dataset_id, c_status)
VALUES ('fset_k_1m', 'crypto', 'dataset_k_1m', '1m', 'dataset_factor_k_1m', 'enabled');
INSERT INTO t_factor_defs (c_factor_id, c_name, c_factor_type, c_source_code, c_source_hash, c_input_columns_json,
    c_outputs_json, c_lookback_periods, c_ctime)
VALUES ('Bias', 'Bias', 'timeseries', 'def compute(): pass', 'sha256:old', '["close"]', '["bias_20"]', 20, '2026-01-02 03:04:05'),
       ('Cci', 'Cci', 'timeseries', 'def compute(): pass', 'sha256:old', '["high","low","close"]', '["cci"]', 39, '2026-01-02 03:04:05');
INSERT INTO t_factor_set_members (c_set_id, c_factor_id, c_status) VALUES ('fset_k_1m', 'Bias', 'enabled'), ('fset_k_1m', 'Cci', 'disabled');
`

var testLibrary = map[string]LibraryDefinition{
	"Bias": {Name: "Bias 偏离", Spec: "params: {window: 20}\noutputs:\n  bias_{window}: close / ts_mean(close, window)\n"},
	"Cci": {Spec: `params: {window: 20}
let:
  tp: (high + low + close) / 3
  m: ts_mean(tp, window)
outputs:
  cci: (tp - m) / (0.015 * ts_mean(abs(tp - m), window))
`},
}

func legacyDatabase(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "factor.db")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(legacySchema)
	require.NoError(t, err)
	return path
}

func TestMigrateExprRebuildsDefinitionsAndKeepsMembers(t *testing.T) {
	ctx := context.Background()
	path := legacyDatabase(t)
	_, err := Open(&Options{Path: path})
	require.ErrorContains(t, err, "migrate-expr", "旧结构的数据库要求先迁移")

	backup := path + ".bak"
	result, err := MigrateExprWithBackup(ctx, path, backup, testLibrary)
	require.NoError(t, err)
	require.False(t, result.AlreadyMigrated)
	require.Equal(t, []string{"Bias", "Cci"}, result.Migrated)
	require.Equal(t, backup, result.Backup)
	_, err = os.Stat(backup)
	require.NoError(t, err, "迁移前备份")

	s, err := Open(&Options{Path: path})
	require.NoError(t, err)
	defer s.Close()
	bias, err := s.GetFactor(ctx, "Bias")
	require.NoError(t, err)
	require.Equal(t, "Bias 偏离", bias.Name, "库里的展示名覆盖旧名")
	require.Equal(t, []string{"bias_20"}, bias.Outputs)
	require.Equal(t, 20, bias.LookbackPeriods)
	require.Equal(t, 2026, bias.CreatedAt.Year(), "保留创建时间")
	cci, err := s.GetFactor(ctx, "Cci")
	require.NoError(t, err)
	require.Equal(t, "Cci", cci.Name, "库里没写展示名时保留旧名")
	members, err := s.ListMembers(ctx, "fset_k_1m", "")
	require.NoError(t, err)
	require.Len(t, members, 2, "成员不受影响")
	var triggers int
	require.NoError(t, s.db.Raw("SELECT count(*) FROM sqlite_master WHERE type = 'trigger' AND name = 'trg_t_factor_defs_mtime'").Scan(&triggers).Error)
	require.Equal(t, 1, triggers, "重建 mtime 触发器")
	require.NoError(t, s.Close())

	again, err := MigrateExprWithBackup(ctx, path, path+".bak2", testLibrary)
	require.NoError(t, err)
	require.True(t, again.AlreadyMigrated)
	_, err = os.Stat(path + ".bak2")
	require.True(t, os.IsNotExist(err), "已经迁移过时不备份")
}

func TestMigrateExprRollsBackOnMismatch(t *testing.T) {
	ctx := context.Background()
	cases := map[string]map[string]LibraryDefinition{
		"缺定义":    {"Bias": testLibrary["Bias"]},
		"输出列不一致": {"Bias": {Spec: "outputs:\n  bias: close\n"}, "Cci": testLibrary["Cci"]},
		"回看不一致":  {"Bias": {Spec: "outputs:\n  bias_20: ts_mean(close, 5)\n"}, "Cci": testLibrary["Cci"]},
		"编译失败":   {"Bias": {Spec: "outputs:\n  bias_20: nope(close)\n"}, "Cci": testLibrary["Cci"]},
	}
	for name, library := range cases {
		t.Run(name, func(t *testing.T) {
			path := legacyDatabase(t)
			_, err := MigrateExpr(ctx, path, library)
			require.Error(t, err)
			db, err := sql.Open("sqlite", path)
			require.NoError(t, err)
			defer db.Close()
			var columns []string
			rows, err := db.Query("SELECT name FROM pragma_table_info('t_factor_defs')")
			require.NoError(t, err)
			for rows.Next() {
				var column string
				require.NoError(t, rows.Scan(&column))
				columns = append(columns, column)
			}
			require.NoError(t, rows.Close())
			require.Contains(t, strings.Join(columns, ","), "c_source_code", "失败时原库不变")
			var leftovers int
			require.NoError(t, db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name = 't_factor_defs_v2'").Scan(&leftovers))
			require.Zero(t, leftovers)
		})
	}
}
