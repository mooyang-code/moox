package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/glebarez/sqlite"
	adminschema "github.com/mooyang-code/moox/modules/admin/schema"
	"gorm.io/gorm"
)

const defaultInitDBPath = "./data/admin.db"

func printInitError(stderr io.Writer, err error) {
	if stderr == nil {
		stderr = io.Discard
	}
	_ = json.NewEncoder(stderr).Encode(map[string]string{
		"error":   "init_failed",
		"message": err.Error(),
	})
}

func applySchema(dbPath string, rawSQL string) error {
	if dbPath == "" {
		return fmt.Errorf("db path is required")
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return fmt.Errorf("create database directory: %w", err)
	}
	db, err := gorm.Open(sqlite.Open(initSQLiteDSN(dbPath)), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("get sql db: %w", err)
	}
	defer sqlDB.Close()
	if err := db.Transaction(func(tx *gorm.DB) error {
		return tx.Exec(rawSQL).Error
	}); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	return nil
}

func ensureAdminSchema(dbPath string) error {
	return applySchema(dbPath, adminschema.AdminSQL())
}

func initSQLiteDSN(dbPath string) string {
	pragmas := []string{
		// 命令行和运行中的 Admin 同时用这个库：事务一开始就拿写锁（BEGIN IMMEDIATE），拿不到按 busy_timeout 等。
		// 默认的延迟事务先读后写，中间 Admin 提交过一次就立即报 database is locked，不等待（重跑部署时 bootstrap 会撞上）。
		"_txlock=immediate",
		"_pragma=foreign_keys(1)",
		"_pragma=journal_mode(WAL)",
		"_pragma=synchronous(FULL)",
		"_pragma=busy_timeout(10000)",
		"_pragma=temp_store(MEMORY)",
		"_pragma=cache_size(-64000)",
		"_pragma=wal_autocheckpoint(1000)",
	}
	sep := "?"
	if strings.Contains(dbPath, "?") {
		sep = "&"
	}
	return dbPath + sep + strings.Join(pragmas, "&")
}
