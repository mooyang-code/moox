// Package storage provides SQLite persistence for moox-cloudnode.
package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/config"
	cloudnodeschema "github.com/mooyang-code/moox/modules/cloudnode/schema"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// Manager owns the cloudnode SQLite connection.
type Manager struct {
	db *gorm.DB
}

// NewManager creates a persistence manager.
func NewManager() *Manager {
	return &Manager{}
}

// Initialize opens SQLite and applies the embedded CloudNode schema.
func (m *Manager) Initialize(dbCfg *config.DatabaseConfig) error {
	dbPath := "./data/moox_cloudnode.db"
	if dbCfg != nil && dbCfg.Path != "" {
		dbPath = dbCfg.Path
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return fmt.Errorf("create database directory: %w", err)
	}
	db, err := gorm.Open(sqlite.Open(buildSQLiteDSN(dbPath)), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	m.db = db
	applySQLitePoolConfig(m.db, dbCfg)
	if err := m.applySchemaSQL("embedded cloudnode schema", cloudnodeschema.AllSQL()); err != nil {
		return err
	}
	if err := m.ensureJobItemProjectionColumns(); err != nil {
		return err
	}
	log.Infof("初始化 CloudNode SQLite 数据库: %s", dbPath)
	return nil
}

// applySchemaSQL applies the given schema text.
func (m *Manager) applySchemaSQL(name string, raw string) error {
	if m.db == nil {
		return fmt.Errorf("database is not initialized")
	}
	if err := m.db.Exec(raw).Error; err != nil {
		return fmt.Errorf("apply schema %s: %w", name, err)
	}
	return nil
}

// DB returns the raw gorm connection.
func (m *Manager) DB() *gorm.DB {
	return m.db
}

// Close closes the underlying SQL connection.
func (m *Manager) Close() error {
	if m.db == nil {
		return nil
	}
	sqlDB, err := m.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

func buildSQLiteDSN(dbPath string) string {
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

func applySQLitePoolConfig(db *gorm.DB, cfg *config.DatabaseConfig) {
	sqlDB, err := db.DB()
	if err != nil {
		return
	}
	if cfg != nil {
		if cfg.MaxOpenConns > 1 || cfg.MaxIdleConns > 1 {
			log.Warnf("CloudNode SQLite 强制使用单连接写入队列: configured max_open_conns=%d max_idle_conns=%d", cfg.MaxOpenConns, cfg.MaxIdleConns)
		}
		if cfg.ConnMaxLifetime > 0 {
			sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)
		}
		if cfg.ConnMaxIdleTime > 0 {
			sqlDB.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)
		}
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
}

func (m *Manager) ensureJobItemProjectionColumns() error {
	columns := map[string]string{
		"c_job_id":             "TEXT NOT NULL DEFAULT ''",
		"c_job_type":           "TEXT NOT NULL DEFAULT ''",
		"c_code_package_id":    "TEXT NOT NULL DEFAULT ''",
		"c_params":             "TEXT NOT NULL DEFAULT '{}'",
		"c_priority":           "INTEGER NOT NULL DEFAULT 0",
		"c_running_node":       "TEXT NOT NULL DEFAULT ''",
		"c_attempt_no":         "INTEGER NOT NULL DEFAULT 0",
		"c_recover_at":         "DATETIME",
		"c_result_summary":     "TEXT NOT NULL DEFAULT '{}'",
		"c_last_error_kind":    "TEXT NOT NULL DEFAULT ''",
		"c_last_error_code":    "TEXT NOT NULL DEFAULT ''",
		"c_last_error_message": "TEXT NOT NULL DEFAULT ''",
		"c_queue_subject":      "TEXT NOT NULL DEFAULT ''",
		"c_queue_msg_id":       "TEXT NOT NULL DEFAULT ''",
		"c_stream_seq":         "INTEGER NOT NULL DEFAULT 0",
		"c_ack_subject":        "TEXT NOT NULL DEFAULT ''",
		"c_enqueue_status":     "TEXT NOT NULL DEFAULT 'queued'",
		"c_control_version":    "INTEGER NOT NULL DEFAULT 0",
		"c_cancel_reason":      "TEXT NOT NULL DEFAULT ''",
		"c_start_time":         "DATETIME",
		"c_finish_time":        "DATETIME",
	}
	for name, definition := range columns {
		if m.db.Migrator().HasColumn("t_cloud_job_items", name) {
			continue
		}
		if err := m.db.Exec(fmt.Sprintf("ALTER TABLE t_cloud_job_items ADD COLUMN %s %s", name, definition)).Error; err != nil {
			return fmt.Errorf("add t_cloud_job_items.%s: %w", name, err)
		}
	}
	indexes := []string{
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_cloud_job_items_space_item ON t_cloud_job_items(c_space_id, c_job_item_id)",
		"CREATE INDEX IF NOT EXISTS idx_cloud_job_items_poll ON t_cloud_job_items(c_space_id, c_status, c_priority, c_ctime)",
		"CREATE INDEX IF NOT EXISTS idx_cloud_job_items_recover ON t_cloud_job_items(c_space_id, c_status, c_recover_at)",
		"CREATE INDEX IF NOT EXISTS idx_cloud_job_items_job ON t_cloud_job_items(c_space_id, c_job_id)",
		"CREATE INDEX IF NOT EXISTS idx_cloud_job_items_enqueue ON t_cloud_job_items(c_space_id, c_enqueue_status, c_status, c_ctime)",
		"CREATE INDEX IF NOT EXISTS idx_cloud_job_items_running ON t_cloud_job_items(c_space_id, c_status, c_running_node, c_recover_at)",
	}
	for _, sql := range indexes {
		if err := m.db.Exec(sql).Error; err != nil {
			return fmt.Errorf("ensure job item projection index: %w", err)
		}
	}
	return nil
}
