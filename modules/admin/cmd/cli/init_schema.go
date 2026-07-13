package main

import (
	"encoding/json"
	"flag"
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

type initResult struct {
	Module string `json:"module"`
	Action string `json:"action"`
	Status string `json:"status"`
	DBPath string `json:"db_path"`
}

func isInitCommand(args []string) bool {
	return len(args) > 1 && args[1] == "init"
}

func runInitCommand(args []string, stdout io.Writer, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "init" {
		return fmt.Errorf("expected init command")
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := defaultInitDBPath
	fs.StringVar(&dbPath, "db-path", dbPath, "SQLite database path")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected init arguments: %s", strings.Join(fs.Args(), " "))
	}
	if err := applySchema(dbPath, adminschema.AdminSQL()); err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(initResult{
		Module: "admin",
		Action: "init",
		Status: "ok",
		DBPath: dbPath,
	})
}

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
		if err := migrateLegacyServiceDeployments(tx); err != nil {
			return err
		}
		return tx.Exec(rawSQL).Error
	}); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	return nil
}

func migrateLegacyServiceDeployments(db *gorm.DB) error {
	var tableCount int64
	if err := db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 't_service_deployments'").Scan(&tableCount).Error; err != nil {
		return err
	}
	if tableCount == 0 {
		return nil
	}
	var legacyColumnCount int64
	if err := db.Raw(`SELECT COUNT(*) FROM pragma_table_info('t_service_deployments') WHERE name IN ('c_is_deleted', 'c_base_url', 'c_rpc_address')`).Scan(&legacyColumnCount).Error; err != nil {
		return err
	}
	if legacyColumnCount == 0 {
		return nil
	}

	return db.Exec(`
DROP TABLE IF EXISTS t_service_deployments_next;
CREATE TABLE t_service_deployments_next (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_service_name TEXT NOT NULL UNIQUE,
    c_service_kind TEXT NOT NULL DEFAULT '',
    c_protocol TEXT NOT NULL DEFAULT 'http',
    c_host TEXT NOT NULL DEFAULT '',
    c_port INTEGER NOT NULL DEFAULT 0,
    c_gateway_path TEXT NOT NULL DEFAULT '',
    c_scope TEXT NOT NULL DEFAULT 'public',
    c_status TEXT NOT NULL DEFAULT 'active',
    c_description TEXT NOT NULL DEFAULT '',
    c_extra_config TEXT NOT NULL DEFAULT '{}',
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO t_service_deployments_next (
    c_id, c_service_name, c_service_kind, c_protocol, c_host, c_port,
    c_gateway_path, c_scope, c_status, c_description, c_extra_config, c_ctime, c_mtime
)
SELECT
    c_id, c_service_name, c_service_kind, c_protocol, c_host, c_port,
    c_gateway_path, c_scope, c_status, c_description, c_extra_config, c_ctime, c_mtime
FROM t_service_deployments
WHERE c_is_deleted = 0;
DROP TABLE t_service_deployments;
ALTER TABLE t_service_deployments_next RENAME TO t_service_deployments;
`).Error
}

func ensureAdminSchema(dbPath string) error {
	return applySchema(dbPath, adminschema.AdminSQL())
}

func initSQLiteDSN(dbPath string) string {
	pragmas := []string{
		"_pragma=journal_mode(WAL)",
		"_pragma=synchronous(OFF)",
		"_pragma=busy_timeout(5000)",
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
