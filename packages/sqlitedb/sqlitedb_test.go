package sqlitedb

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/packages/dataformat"
	"github.com/stretchr/testify/require"
)

const testSchemaV1 = `
-- 示例表
CREATE TABLE IF NOT EXISTS t_items (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_name TEXT NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'active',
    CHECK (c_status IN ('active', 'disabled')),
    UNIQUE (c_name)
);

CREATE INDEX IF NOT EXISTS idx_t_items_status ON t_items (c_status);
`

const testSchemaV1Reformatted = `
-- 修改了注释、缩进、大小写和括号空格，语义完全相同
create table if not exists t_items(
  c_id integer not null primary key autoincrement,
  c_name text not null, -- 行尾注释
  c_status text not null default 'active',
  check(c_status in('active','disabled')),
  unique(c_name)
);
create index if not exists idx_t_items_status on t_items(c_status);
`

const testSchemaV2Changed = `
CREATE TABLE IF NOT EXISTS t_items (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_name TEXT NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'active',
    c_note TEXT NOT NULL DEFAULT '',
    CHECK (c_status IN ('active', 'disabled')),
    UNIQUE (c_name)
);

CREATE INDEX IF NOT EXISTS idx_t_items_status ON t_items (c_status);
`

func TestDSNIncludesUnifiedPragmasAndImmediateTxLock(t *testing.T) {
	rw := DSN(Options{Path: "/tmp/a.db", Synchronous: "FULL", BusyTimeoutMS: 10000})
	for _, want := range []string{
		"_txlock=immediate",
		"_pragma=foreign_keys(ON)",
		"_pragma=journal_mode(WAL)",
		"_pragma=synchronous(FULL)",
		"_pragma=busy_timeout(10000)",
		"_pragma=temp_store(MEMORY)",
		"_pragma=cache_size(-64000)",
		"_pragma=wal_autocheckpoint(1000)",
	} {
		require.Contains(t, rw, want)
	}

	ro := DSN(Options{Path: "/tmp/a.db", ReadOnly: true})
	require.Contains(t, ro, "_pragma=query_only(1)")
	require.NotContains(t, ro, "_txlock=immediate")

	av := DSN(Options{Path: "/tmp/a.db", AutoVacuum: "INCREMENTAL"})
	require.Less(t, indexOf(av, "auto_vacuum(INCREMENTAL)"), indexOf(av, "journal_mode(WAL)"))
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestSchemaHashIgnoresFormattingOnlyChangesAndDetectsSemanticChanges(t *testing.T) {
	h1, err := SchemaHash(testSchemaV1)
	require.NoError(t, err)
	hReformatted, err := SchemaHash(testSchemaV1Reformatted)
	require.NoError(t, err)
	require.Equal(t, h1, hReformatted, "纯格式调整不能改变 schema 哈希")

	h2, err := SchemaHash(testSchemaV2Changed)
	require.NoError(t, err)
	require.NotEqual(t, h1, h2, "表结构语义变更必须改变 schema 哈希")
}

func TestEnsureSchemaWritesFormatAcceptsReformattedAndRejectsChanged(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "demo.db")

	db, err := OpenSQL(Options{Path: dbPath})
	require.NoError(t, err)
	require.NoError(t, EnsureSchemaSQL(context.Background(), db, dbPath, "demo", testSchemaV1, dataformat.LifecycleParts()))
	require.NoError(t, db.Close())

	rec, err := dataformat.ReadFile(filepath.Join(dir, dataformat.FileName))
	require.NoError(t, err)
	require.Equal(t, "demo", rec.Component)
	h1, err := SchemaHash(testSchemaV1)
	require.NoError(t, err)
	require.Equal(t, h1, rec.Parts[dataformat.PartSQLiteSchema])
	require.Equal(t, LifecycleVersion(), rec.Parts[dataformat.PartLifecycle])

	// 用纯格式调整后的 schema 重开：校验通过。
	db2, err := OpenSQL(Options{Path: dbPath})
	require.NoError(t, err)
	require.NoError(t, EnsureSchemaSQL(context.Background(), db2, dbPath, "demo", testSchemaV1Reformatted, dataformat.LifecycleParts()))
	require.NoError(t, db2.Close())

	// 用结构变更后的 schema 重开：拒绝启动且提示全新部署。
	db3, err := OpenSQL(Options{Path: dbPath})
	require.NoError(t, err)
	defer db3.Close()
	err = EnsureSchemaSQL(context.Background(), db3, dbPath, "demo", testSchemaV2Changed, dataformat.LifecycleParts())
	require.ErrorIs(t, err, ErrIncompatibleSchema)
	require.Contains(t, err.Error(), "全新部署")
}

func TestValidateTablesSQLIgnoresDynamicTables(t *testing.T) {
	db, err := OpenSQL(Options{Path: ":memory:"})
	require.NoError(t, err)
	defer db.Close()

	fixed := `CREATE TABLE IF NOT EXISTS task_state (id TEXT PRIMARY KEY) WITHOUT ROWID;`
	_, err = db.Exec(fixed + ` CREATE TABLE staged_abc (k TEXT PRIMARY KEY);`)
	require.NoError(t, err)

	require.NoError(t, ValidateTablesSQL(context.Background(), db, "archive-worker", fixed, false))

	changedFixed := `CREATE TABLE IF NOT EXISTS task_state (id TEXT PRIMARY KEY, extra TEXT NOT NULL DEFAULT '') WITHOUT ROWID;`
	err = ValidateTablesSQL(context.Background(), db, "archive-worker", changedFixed, false)
	require.ErrorIs(t, err, ErrIncompatibleSchema)
}
