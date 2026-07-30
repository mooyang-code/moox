package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	"github.com/stretchr/testify/require"
)

func TestOpenAndClose(t *testing.T) {
	mgr, err := Open(&Options{Path: filepath.Join(t.TempDir(), "factor.db")})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if mgr.db == nil {
		t.Fatal("database returned nil")
	}
	if err := mgr.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestForeignKeysAreEnabledOnEveryPooledConnection(t *testing.T) {
	db, err := Open(&Options{
		Path: filepath.Join(t.TempDir(), "factor.db"), MaxOpenConns: 4, MaxIdleConns: 4,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(factorschema.AllSQL()))

	sqlDB, err := db.db.DB()
	require.NoError(t, err)
	conns := make([]interface{ Close() error }, 0, 4)
	for index := range 4 {
		conn, connErr := sqlDB.Conn(context.Background())
		require.NoError(t, connErr)
		conns = append(conns, conn)
		var enabled int
		require.NoError(t, conn.QueryRowContext(context.Background(), "PRAGMA foreign_keys").Scan(&enabled))
		require.Equal(t, 1, enabled)
		_, insertErr := conn.ExecContext(context.Background(), `
			INSERT INTO t_factor_bindings (
				c_binding_id, c_factor_id, c_space_id, c_source_dataset, c_freq,
				c_subject_mode, c_subjects_json, c_target_dataset, c_status
			) VALUES (?, 'missing', 'space', 'source', '1m', 'all', '[]', 'target', 'disabled')
		`, "orphan-"+string(rune('a'+index)))
		require.ErrorContains(t, insertErr, "FOREIGN KEY constraint failed")
	}
	for _, conn := range conns {
		require.NoError(t, conn.Close())
	}
}

func TestBuildSQLiteDSNUsesDurablePragmas(t *testing.T) {
	dsn := buildSQLiteDSN("./data/factor/factor.db")

	for _, want := range []string{
		"_pragma=journal_mode(WAL)",
		"_pragma=foreign_keys(ON)",
		"_pragma=synchronous(NORMAL)",
		"_pragma=busy_timeout(5000)",
		"_pragma=temp_store(MEMORY)",
		"_pragma=cache_size(-64000)",
		"_pragma=wal_autocheckpoint(1000)",
	} {
		if !strings.Contains(dsn, want) {
			t.Fatalf("dsn %q does not contain %q", dsn, want)
		}
	}
	if strings.Contains(dsn, "synchronous(OFF)") {
		t.Fatalf("dsn %q must not disable SQLite synchronization", dsn)
	}
}

func TestApplySchemaRejectsObsoleteDatabase(t *testing.T) {
	db, err := Open(&Options{Path: filepath.Join(t.TempDir(), "factor.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.db.Exec(`
		CREATE TABLE t_factor_defs (
			c_factor_id TEXT PRIMARY KEY,
			c_name TEXT,
			c_params_json TEXT
		);
		CREATE TABLE t_factor_event_inbox (c_event_id TEXT PRIMARY KEY);
	`).Error)

	err = db.ApplySchema(factorschema.AllSQL())
	require.ErrorContains(t, err, "fresh database")
}

func TestApplySchemaRejectsLookbackRowsDatabase(t *testing.T) {
	db, err := Open(&Options{Path: filepath.Join(t.TempDir(), "factor.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.db.Exec(`
		CREATE TABLE t_factor_defs (
			c_factor_id TEXT NOT NULL PRIMARY KEY,
			c_name TEXT NOT NULL,
			c_source_code TEXT NOT NULL,
			c_source_hash TEXT NOT NULL,
			c_source_path TEXT NOT NULL DEFAULT '',
			c_input_columns_json TEXT NOT NULL,
			c_outputs_json TEXT NOT NULL,
			c_params_json TEXT NOT NULL DEFAULT '{}',
			c_lookback_rows INTEGER NOT NULL,
			c_status TEXT NOT NULL,
			c_ctime DATETIME NOT NULL,
			c_mtime DATETIME NOT NULL
		);
		CREATE TABLE t_factor_bindings (
			c_binding_id TEXT NOT NULL PRIMARY KEY,
			c_factor_id TEXT NOT NULL,
			c_space_id TEXT NOT NULL,
			c_source_dataset TEXT NOT NULL,
			c_freq TEXT NOT NULL,
			c_subject_mode TEXT NOT NULL,
			c_subjects_json TEXT NOT NULL,
			c_target_dataset TEXT NOT NULL,
			c_status TEXT NOT NULL,
			c_ctime DATETIME NOT NULL,
			c_mtime DATETIME NOT NULL
		);
	`).Error)

	err = db.ApplySchema(factorschema.AllSQL())
	require.ErrorContains(t, err, "fresh database")
}
