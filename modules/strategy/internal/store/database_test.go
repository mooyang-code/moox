package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"gorm.io/gorm"
)

func TestOpenApplySchemaAndClose(t *testing.T) {
	mgr, err := Open(filepath.Join(t.TempDir(), "strategy.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := mgr.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatalf("ApplySchema() error = %v", err)
	}
	if mgr.db == nil {
		t.Fatal("DB() returned nil")
	}
	if err := mgr.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestOpenRejectsMalformedCurrentStrategySchema(t *testing.T) {
	tests := map[string]func(string) string{
		"missing primary key": func(sql string) string {
			return strings.Replace(sql, "strategy_id TEXT PRIMARY KEY", "strategy_id TEXT NOT NULL", 1)
		},
		"missing not null": func(sql string) string {
			return strings.Replace(sql, "source_hash TEXT NOT NULL", "source_hash TEXT", 1)
		},
		"missing result logical index": func(sql string) string {
			return strings.Replace(sql, "CREATE INDEX IF NOT EXISTS ix_strategy_results_logical_period\nON t_strategy_results (runner_id, strategy_id, period_time, created_at);", "", 1)
		},
		"runner owner index missing space": func(sql string) string {
			return strings.Replace(
				sql,
				"(space_id, logical_account_id)",
				"(logical_account_id)",
				1,
			)
		},
		"wrong runner partial unique predicate": func(sql string) string {
			return strings.Replace(sql, "status = 'ENABLED'", "status = 'DISABLED'", 1)
		},
		"case changed runner partial unique predicate": func(sql string) string {
			return strings.Replace(sql, "status = 'ENABLED'", "status = 'enabled'", 1)
		},
		"broadened runner partial unique predicate": func(sql string) string {
			return strings.Replace(
				sql,
				"status = 'ENABLED'",
				"(status = 'ENABLED' OR status = 'DISABLED')",
				1,
			)
		},
		"partial result logical index": func(sql string) string {
			sql = strings.Replace(sql, "CREATE INDEX IF NOT EXISTS ix_strategy_results_logical_period\nON t_strategy_results (runner_id, strategy_id, period_time, created_at);", "", 1)
			return sql + `
CREATE INDEX ix_strategy_results_logical_period
ON t_strategy_results (runner_id, strategy_id, period_time, created_at)
WHERE 0;
`
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "strategy.db")
			db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Exec(mutate(schema.AllSQL())).Error; err != nil {
				t.Fatal(err)
			}
			sqlDB, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			if err := sqlDB.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "删除旧数据库后重建") {
				t.Fatalf("Open() error = %v", err)
			}
		})
	}
}

func TestOpenRejectsObsoleteStrategySchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "strategy.db")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE t_strategy_bindings (c_binding_id TEXT PRIMARY KEY)").Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = Open(path)
	if err == nil {
		t.Fatal("Open() accepted an obsolete Strategy schema")
	}
	for _, want := range []string{"t_strategy_bindings", "删除旧数据库后重建"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Open() error = %q, want it to contain %q", err, want)
		}
	}
}

func TestOpenAcceptsCurrentSchemaOnReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "strategy.db")
	first, err := Open(path)
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	if err := first.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatalf("ApplySchema() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopened Open() error = %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("reopened Close() error = %v", err)
	}
}

func TestOpenArchivesLegacyV1SchemaBeforeApplyingV2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "strategy.db")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	legacySQL := `
CREATE TABLE t_strategies (
    strategy_id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    manifest_yaml TEXT NOT NULL,
    source_code TEXT NOT NULL,
    source_hash TEXT NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE TABLE t_strategy_runners (
    runner_id TEXT PRIMARY KEY,
    strategy_id TEXT NOT NULL,
    space_id TEXT NOT NULL,
    view_id TEXT NOT NULL,
    frequency TEXT NOT NULL,
    params_json TEXT NOT NULL,
    logical_account_id TEXT,
    status TEXT NOT NULL,
    current_targets_json TEXT NOT NULL,
    command_sequence INTEGER NOT NULL,
    last_result_id TEXT,
    last_success_at INTEGER,
    last_error TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX ux_strategy_runners_enabled_logical_account
ON t_strategy_runners (space_id, logical_account_id)
WHERE logical_account_id IS NOT NULL AND status = 'ENABLED';
CREATE TABLE t_strategy_results (
    result_id TEXT PRIMARY KEY,
    runner_id TEXT NOT NULL,
    strategy_id TEXT NOT NULL,
    trigger_bar_time INTEGER NOT NULL,
    namespace TEXT NOT NULL,
    input_hash TEXT NOT NULL,
    action TEXT NOT NULL,
    output_json TEXT NOT NULL,
    command_sequence INTEGER,
    created_at INTEGER NOT NULL,
    UNIQUE (runner_id, strategy_id, namespace, trigger_bar_time)
);
CREATE TABLE t_strategy_outbox (
    message_id TEXT PRIMARY KEY,
    event_data BLOB NOT NULL,
    created_at INTEGER NOT NULL
);
INSERT INTO t_strategies(strategy_id, name, manifest_yaml, source_code, source_hash, created_at)
VALUES ('legacy-1', 'legacy', 'manifest', 'print(1)', 'hash', 1);
INSERT INTO t_strategy_runners(
 runner_id, strategy_id, space_id, view_id, frequency, params_json,
 logical_account_id, status, current_targets_json, command_sequence,
 created_at, updated_at
) VALUES ('legacy-runner', 'legacy-1', 'space', 'view', '1m', '{}',
 'logical', 'ENABLED', '[]', 0, 1, 1);
`
	if err := db.Exec(legacySQL).Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}

	mgr, err := Open(path)
	if err != nil {
		t.Fatalf("Open() should archive the V1 schema: %v", err)
	}
	defer func() { _ = mgr.Close() }()
	if err := mgr.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatalf("ApplySchema() error = %v", err)
	}
	var archivedCount int64
	if err := mgr.db.Raw(`SELECT COUNT(*) FROM legacy_strategy_v1_strategies WHERE strategy_id = 'legacy-1'`).Scan(&archivedCount).Error; err != nil {
		t.Fatal(err)
	}
	if archivedCount != 1 {
		t.Fatalf("archived legacy strategy count = %d, want 1", archivedCount)
	}
	owners, err := mgr.ListLegacyRunnerOwners(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(owners) != 1 || owners[0].RunnerID != "legacy-runner" || owners[0].LogicalAccountID != "logical" {
		t.Fatalf("archived owners = %+v", owners)
	}
	var activeTables int64
	if err := mgr.db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND (name = 't_strategies' OR name LIKE 't_strategy_%')`).Scan(&activeTables).Error; err != nil {
		t.Fatal(err)
	}
	if activeTables != int64(len(strategySchemaColumns)) {
		t.Fatalf("active Strategy tables = %d, want %d", activeTables, len(strategySchemaColumns))
	}
}

func TestOpenRejectsMixedLegacyAndCurrentStrategySchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mixed.db")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`
CREATE TABLE t_strategies (strategy_id TEXT PRIMARY KEY, source_code TEXT NOT NULL);
CREATE TABLE t_strategy_inbox (message_id TEXT PRIMARY KEY, event_name TEXT NOT NULL, received_at INTEGER NOT NULL);
`).Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = Open(path)
	if err == nil || !strings.Contains(err.Error(), "同时包含 V1 与 V2") {
		t.Fatalf("Open() error = %v, want mixed-schema rejection", err)
	}
}

func TestOpenRejectsMalformedPureLegacyStrategySchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "malformed-legacy.db")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`
CREATE TABLE t_strategies (
    strategy_id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    manifest_yaml TEXT NOT NULL,
    source_code TEXT NOT NULL,
    created_at INTEGER NOT NULL
);
`).Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = Open(path)
	if err == nil || !strings.Contains(err.Error(), "结构不完整") {
		t.Fatalf("Open() error = %v, want malformed V1 rejection", err)
	}
}
