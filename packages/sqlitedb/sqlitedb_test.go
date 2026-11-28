package sqlitedb

import (
	"context"
	"os"
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

func TestSchemaIncludesTriggersAndViews(t *testing.T) {
	base := `
CREATE TABLE IF NOT EXISTS t_items (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_name TEXT NOT NULL,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TRIGGER IF NOT EXISTS trg_t_items_mtime
AFTER UPDATE ON t_items
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_items SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

CREATE VIEW IF NOT EXISTS v_items AS
SELECT c_id, c_name FROM t_items;
`
	reformatted := `
create table if not exists t_items(
  c_id integer not null primary key autoincrement,
  c_name text not null,
  c_mtime datetime not null default current_timestamp
);
-- 仅排版调整
create trigger if not exists trg_t_items_mtime
after update on t_items for each row when new.c_mtime=old.c_mtime begin
  update t_items set c_mtime=current_timestamp where c_id=old.c_id;
end;
create view if not exists v_items as select c_id,c_name from t_items;
`
	changedTrigger := `
CREATE TABLE IF NOT EXISTS t_items (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_name TEXT NOT NULL,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TRIGGER IF NOT EXISTS trg_t_items_mtime
AFTER UPDATE ON t_items
FOR EACH ROW
BEGIN
    UPDATE t_items SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

CREATE VIEW IF NOT EXISTS v_items AS
SELECT c_id, c_name FROM t_items;
`
	changedView := `
CREATE TABLE IF NOT EXISTS t_items (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_name TEXT NOT NULL,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TRIGGER IF NOT EXISTS trg_t_items_mtime
AFTER UPDATE ON t_items
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_items SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

CREATE VIEW IF NOT EXISTS v_items AS
SELECT c_id, c_name, c_mtime FROM t_items;
`
	hBase, err := SchemaHash(base)
	require.NoError(t, err)
	hReformatted, err := SchemaHash(reformatted)
	require.NoError(t, err)
	require.Equal(t, hBase, hReformatted, "触发器与视图纯格式调整不改变哈希")

	hTrigger, err := SchemaHash(changedTrigger)
	require.NoError(t, err)
	require.NotEqual(t, hBase, hTrigger, "触发器语义变化必须改变哈希")

	hView, err := SchemaHash(changedView)
	require.NoError(t, err)
	require.NotEqual(t, hBase, hView, "视图语义变化必须改变哈希")

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "tv.db")
	db, err := OpenSQL(Options{Path: dbPath})
	require.NoError(t, err)
	require.NoError(t, EnsureSchemaSQL(context.Background(), db, dbPath, "demo", base, nil))
	require.NoError(t, db.Close())

	// 触发器语义变化时启动校验必须拒绝。
	db2, err := OpenSQL(Options{Path: dbPath})
	require.NoError(t, err)
	err = EnsureSchemaSQL(context.Background(), db2, dbPath, "demo", changedTrigger, nil)
	require.ErrorIs(t, err, ErrIncompatibleSchema)
	require.ErrorContains(t, err, "触发器")
	require.NoError(t, db2.Close())

	// 视图语义变化时启动校验必须拒绝。
	db3, err := OpenSQL(Options{Path: dbPath})
	require.NoError(t, err)
	defer db3.Close()
	err = EnsureSchemaSQL(context.Background(), db3, dbPath, "demo", changedView, nil)
	require.ErrorIs(t, err, ErrIncompatibleSchema)
	require.ErrorContains(t, err, "视图")
}

func TestEnsureSchemaRejectsLifecycleVersionMismatchOnExistingData(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "demo.db")

	db, err := OpenSQL(Options{Path: dbPath})
	require.NoError(t, err)
	require.NoError(t, EnsureSchemaSQL(context.Background(), db, dbPath, "demo", testSchemaV1, map[string]string{dataformat.PartLifecycle: "1"}))
	require.NoError(t, db.Close())

	// 已有数据且 schema 未变，但 lifecycle 版本常量改变：必须拒绝启动并提示 setup install --wipe。
	db2, err := OpenSQL(Options{Path: dbPath})
	require.NoError(t, err)
	defer db2.Close()
	err = EnsureSchemaSQL(context.Background(), db2, dbPath, "demo", testSchemaV1, map[string]string{dataformat.PartLifecycle: "2"})
	require.ErrorIs(t, err, dataformat.ErrIncompatibleFormat)
	require.ErrorContains(t, err, dataformat.PartLifecycle)
	require.ErrorContains(t, err, "setup install --wipe")
}

// 新版本仅新增表时，核对格式文件发生在建表事务之前：拒绝启动且不得先把新表建进旧库，之后旧二进制仍能正常启动。
func TestEnsureSchemaVerifiesFormatBeforeApplyingNewTables(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "demo.db")

	dbV1, err := OpenSQL(Options{Path: dbPath})
	require.NoError(t, err)
	require.NoError(t, EnsureSchemaSQL(context.Background(), dbV1, dbPath, "demo", testSchemaV1, nil))
	require.NoError(t, dbV1.Close())

	schemaWithExtraTable := testSchemaV1 + `
CREATE TABLE IF NOT EXISTS t_extra_v2 (
    c_id INTEGER NOT NULL PRIMARY KEY
);
`
	dbV2, err := OpenSQL(Options{Path: dbPath})
	require.NoError(t, err)
	err = EnsureSchemaSQL(context.Background(), dbV2, dbPath, "demo", schemaWithExtraTable, nil)
	require.ErrorIs(t, err, dataformat.ErrIncompatibleFormat)
	require.ErrorContains(t, err, dataformat.PartSQLiteSchema)
	require.ErrorContains(t, err, "setup install --wipe")

	// 新表绝不能已经被建进旧库。
	objects, err := readSchemaObjects(context.Background(), dbV2)
	require.NoError(t, err)
	_, hasExtra := objects["table\x00t_extra_v2"]
	require.False(t, hasExtra, "格式不符拒绝启动时不得先把新增表建进已有库")
	require.NoError(t, dbV2.Close())

	// 回退旧二进制（testSchemaV1）仍可正常启动。
	dbRollback, err := OpenSQL(Options{Path: dbPath})
	require.NoError(t, err)
	defer dbRollback.Close()
	require.NoError(t, EnsureSchemaSQL(context.Background(), dbRollback, dbPath, "demo", testSchemaV1, nil))
}

// EnsureSchemaSQL 按调用方传入的实际状态文件路径判断是否已有本地数据，不再猜测同目录的 lifecycle.json。
func TestEnsureSchemaUsesExplicitExtraDataPaths(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "demo.db")
	actualStateFile := filepath.Join(dir, "state", "lifecycle.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(actualStateFile), 0o755))
	require.NoError(t, os.WriteFile(actualStateFile, []byte(`{"stage":"paused"}`), 0o600))

	// 先在数据目录里放一份旧版本 data-format（lifecycle=1），但库里还没有任何表。
	oldRec, err := FormatRecord("demo", testSchemaV1, map[string]string{dataformat.PartLifecycle: "1"})
	require.NoError(t, err)
	require.NoError(t, dataformat.WriteDir(dir, oldRec))

	// 传入实际状态文件路径：识别为“已有数据”，在建表前即因 lifecycle 版本不符拒绝启动，且不建表。
	db, err := OpenSQL(Options{Path: dbPath})
	require.NoError(t, err)
	err = EnsureSchemaSQL(context.Background(), db, dbPath, "demo", testSchemaV1, map[string]string{dataformat.PartLifecycle: "2"}, actualStateFile)
	require.ErrorIs(t, err, dataformat.ErrIncompatibleFormat)
	objects, err := readSchemaObjects(context.Background(), db)
	require.NoError(t, err)
	require.Empty(t, objects, "建表前拒启时不应在空库里建出表")
	require.NoError(t, db.Close())

	// 若仅在同目录放一个未传入的 lifecycle.json，sqlitedb 不应擅自猜测它。
	dir2 := t.TempDir()
	dbPath2 := filepath.Join(dir2, "demo.db")
	require.NoError(t, os.WriteFile(filepath.Join(dir2, "lifecycle.json"), []byte(`{"stage":"paused"}`), 0o600))
	require.NoError(t, dataformat.WriteDir(dir2, oldRec))

	db2, err := OpenSQL(Options{Path: dbPath2})
	require.NoError(t, err)
	defer db2.Close()
	require.NoError(t, EnsureSchemaSQL(context.Background(), db2, dbPath2, "demo", testSchemaV1, map[string]string{dataformat.PartLifecycle: "2"}))
}
