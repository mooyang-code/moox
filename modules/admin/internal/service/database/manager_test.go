package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/admin/internal/config"
	"github.com/mooyang-code/moox/packages/dataformat"
	"github.com/mooyang-code/moox/packages/sqlitedb"
	"github.com/stretchr/testify/require"
)

func TestInitializeAppliesSchemaWritesFormatAndRejectsObsolete(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "admin.db")
	mgr := NewManager()
	require.NoError(t, mgr.Initialize(&config.DatabaseConfig{Path: dbPath}))
	sqlDB, err := mgr.GetDB().DB()
	require.NoError(t, err)
	defer sqlDB.Close()

	var count int64
	require.NoError(t, mgr.GetDB().Raw(`
SELECT count(*)
FROM sqlite_master
WHERE type = 'table'
  AND name IN ('t_users', 't_secrets', 't_hosts', 't_deployments')
`).Scan(&count).Error)
	require.Equal(t, int64(4), count)

	rec, err := dataformat.ReadFile(filepath.Join(dir, dataformat.FileName))
	require.NoError(t, err)
	require.Equal(t, "admin", rec.Component)
	require.NotEmpty(t, rec.Parts[dataformat.PartSQLiteSchema])

	// 结构不符的旧库必须拒绝启动。
	badPath := filepath.Join(t.TempDir(), "old-admin.db")
	badDB, err := sqlitedb.OpenSQL(sqlitedb.Options{Path: badPath})
	require.NoError(t, err)
	_, err = badDB.Exec(`CREATE TABLE t_users (c_id INTEGER PRIMARY KEY)`)
	require.NoError(t, err)
	require.NoError(t, badDB.Close())

	err = NewManager().Initialize(&config.DatabaseConfig{Path: badPath})
	require.ErrorIs(t, err, sqlitedb.ErrIncompatibleSchema)
}

func TestInitializeEnforcesForeignKeysOnEveryConnection(t *testing.T) {
	mgr := NewManager()
	if err := mgr.Initialize(&config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "admin.db")}); err != nil {
		t.Fatal(err)
	}
	db, err := mgr.GetDB().DB()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Keep both leases open so the pool must create distinct connections.
	first, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	for _, conn := range []*sql.Conn{first, second} {
		var enabled int
		if err := conn.QueryRowContext(context.Background(), "PRAGMA foreign_keys").Scan(&enabled); err != nil || enabled != 1 {
			t.Fatalf("foreign keys = %d, error = %v", enabled, err)
		}
		if err := conn.QueryRowContext(context.Background(), "PRAGMA synchronous").Scan(&enabled); err != nil || enabled != 2 {
			t.Fatalf("synchronous = %d, error = %v; want FULL on every connection", enabled, err)
		}
	}
}

func TestInitializeUsesBoundedSQLitePool(t *testing.T) {
	mgr := NewManager()
	if err := mgr.Initialize(&config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "admin.db")}); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	sqlDB, err := mgr.GetDB().DB()
	if err != nil {
		t.Fatalf("DB() error = %v", err)
	}
	defer sqlDB.Close()
	stats := sqlDB.Stats()
	if stats.MaxOpenConnections != 8 {
		t.Fatalf("MaxOpenConnections = %d, want 8", stats.MaxOpenConnections)
	}
}
