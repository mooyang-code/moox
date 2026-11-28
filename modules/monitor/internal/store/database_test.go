package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	_ "github.com/glebarez/go-sqlite"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/dataformat"
	"github.com/mooyang-code/moox/packages/sqlitedb"
)

func TestApplySchemaWritesFormatAndRejectsIncompatibleDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "monitor.db")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("打开监控数据库失败：%v", err)
	}
	if err := s.ApplySchema(schema.SQL()); err != nil {
		t.Fatalf("应用监控 schema 失败：%v", err)
	}
	_ = s.Close()

	rec, err := dataformat.ReadFile(filepath.Join(dir, dataformat.FileName))
	if err != nil {
		t.Fatalf("读取数据格式文件失败：%v", err)
	}
	if rec.Component != "monitor" || rec.Parts[dataformat.PartSQLiteSchema] == "" || rec.Parts[dataformat.PartLifecycle] == "" {
		t.Fatalf("数据格式记录异常：%+v", rec)
	}

	// 制造旧表结构，确认再次启动时被拒绝。
	badPath := filepath.Join(t.TempDir(), "old-monitor.db")
	raw, err := sql.Open("sqlite", badPath)
	if err != nil {
		t.Fatalf("创建旧库失败：%v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE t_legacy_alerts (c_id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("写入旧表失败：%v", err)
	}
	_ = raw.Close()

	badStore, err := Open(badPath)
	if err != nil {
		t.Fatalf("打开旧库失败：%v", err)
	}
	defer func() { _ = badStore.Close() }()
	if err := badStore.ApplySchema(schema.SQL()); !errors.Is(err, sqlitedb.ErrIncompatibleSchema) {
		t.Fatalf("旧表结构应被拒绝，实际返回：%v", err)
	}
}
