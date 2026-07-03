package storage

import (
	"fmt"
	"strings"

	"trpc.group/trpc-go/trpc-go/log"
)

// migrationStatements are idempotent schema upgrades for existing SQLite databases.
// CREATE TABLE IF NOT EXISTS does not alter existing tables, so we apply additive migrations here.
var migrationStatements = []string{
	`ALTER TABLE t_cloud_nodes ADD COLUMN c_package_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE t_cloud_nodes ADD COLUMN c_package_version TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE t_cloud_work_items ADD COLUMN c_lease_timeout_ms INTEGER NOT NULL DEFAULT 600000`,
	`UPDATE t_cloud_nodes SET c_is_deleted = CASE
		WHEN c_is_deleted IN ('1', 'true', 'TRUE', 'yes', 'YES') THEN 1
		ELSE 0
	END WHERE typeof(c_is_deleted) = 'text'`,
	`UPDATE t_cloud_accounts SET c_is_deleted = CASE
		WHEN c_is_deleted IN ('1', 'true', 'TRUE', 'yes', 'YES') THEN 1
		ELSE 0
	END WHERE typeof(c_is_deleted) = 'text'`,
	`UPDATE t_cloud_function_packages SET c_is_deleted = CASE
		WHEN c_is_deleted IN ('1', 'true', 'TRUE', 'yes', 'YES') THEN 1
		ELSE 0
	END WHERE typeof(c_is_deleted) = 'text'`,
}

func (m *Manager) applyMigrations() error {
	if m.db == nil {
		return fmt.Errorf("database is not initialized")
	}
	for _, stmt := range migrationStatements {
		if err := m.db.Exec(stmt).Error; err != nil {
			if isIgnorableMigrationError(err) {
				continue
			}
			return fmt.Errorf("apply migration: %w", err)
		}
	}
	log.Info("CloudNode SQLite migrations applied")
	return nil
}

func isIgnorableMigrationError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate column") ||
		strings.Contains(msg, "already exists")
}
