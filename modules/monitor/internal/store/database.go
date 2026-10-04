// Package store owns monitor's SQLite connection and persistence repositories.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	"gorm.io/gorm"
)

type Store struct {
	db *gorm.DB
}

func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("database path is required")
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("open monitor database: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("open sql database: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	sqlDB.SetConnMaxLifetime(time.Hour)
	if err := db.Exec("PRAGMA journal_mode=WAL").Error; err != nil {
		return nil, fmt.Errorf("enable WAL: %w", err)
	}
	if err := db.Exec("PRAGMA busy_timeout=5000").Error; err != nil {
		return nil, fmt.Errorf("set busy timeout: %w", err)
	}
	return &Store{db: db}, nil
}

func OpenFromConfig(cfg config.DatabaseConfig) (*Store, error) {
	mgr, err := Open(cfg.Path)
	if err != nil {
		return nil, err
	}
	sqlDB, err := mgr.db.DB()
	if err != nil {
		_ = mgr.Close()
		return nil, fmt.Errorf("open sql database: %w", err)
	}
	if cfg.MaxOpenConns > 0 {
		sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	}
	if cfg.MaxIdleConns > 0 {
		sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	}
	if cfg.ConnMaxLifetime > 0 {
		sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	}
	if cfg.ConnMaxIdleTime > 0 {
		sqlDB.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)
	}
	return mgr, nil
}

// Repositories builds the monitor control-plane repository graph.
func (m *Store) Repositories() *Repositories {
	if m == nil {
		return nil
	}
	return NewRepositories(m.db)
}

// WithDatabase runs a short-lived construction callback against the private
// database handle. Production callers should prefer Repositories or domain
// methods; this escape hatch is reserved for bounded-context adapters.
func WithDatabase[T any](m *Store, fn func(*gorm.DB) T) (T, error) {
	var zero T
	if m == nil || m.db == nil {
		return zero, fmt.Errorf("monitor database is not open")
	}
	if fn == nil {
		return zero, fmt.Errorf("database callback is nil")
	}
	return fn(m.db), nil
}

// Ping verifies that the database is available.
func (m *Store) Ping(ctx context.Context) error {
	if m == nil || m.db == nil {
		return fmt.Errorf("monitor database is not open")
	}
	return m.db.WithContext(ctx).Exec("SELECT 1").Error
}

func (m *Store) ApplySchema(sql string) error {
	if m == nil || m.db == nil {
		return fmt.Errorf("monitor database is not open")
	}
	if strings.TrimSpace(sql) == "" {
		return fmt.Errorf("schema sql is empty")
	}
	return m.db.Exec(sql).Error
}

// ResetLegacyMonitorTables detects the pre-global-notification alert schema.
// The project is greenfield, so an old database is rebuilt rather than
// attempting an unsafe SQLite ALTER that could leave a required webhook
// column behind. The caller must apply the current schema afterwards.
func (m *Store) ResetLegacyMonitorTables() (bool, error) {
	if m == nil || m.db == nil {
		return false, fmt.Errorf("monitor database is not open")
	}
	rows, err := m.db.Raw("PRAGMA table_info(t_monitor_alert_rules)").Rows()
	if err != nil {
		return false, err
	}
	legacy := false
	for rows.Next() {
		var cid, notNull, pk int
		var name, columnType sql.NullString
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			_ = rows.Close()
			return false, err
		}
		if name.Valid && name.String == "c_webhook_id" {
			legacy = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return false, err
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	if !legacy {
		return false, nil
	}
	for _, table := range []string{
		"t_monitor_checks", "t_monitor_check_results", "t_monitor_alert_rules",
		"t_monitor_alert_states", "t_monitor_alert_events",
	} {
		if err := m.db.Exec("DROP TABLE IF EXISTS " + table).Error; err != nil {
			return false, fmt.Errorf("reset legacy monitor table %s: %w", table, err)
		}
	}
	return true, nil
}

// DropRetiredTables removes greenfield-era tables that no longer have a
// runtime owner. This is intentionally destructive: the project has not
// shipped a compatibility migration and these tables contain no supported
// user data.
func (m *Store) DropRetiredTables() error {
	if m == nil || m.db == nil {
		return fmt.Errorf("monitor database is not open")
	}
	for _, table := range []string{
		"t_monitor_webhooks",
		"t_monitor_metric_rules",
		"t_monitor_metric_rule_states",
		"t_monitor_metric_rule_evaluations",
		"t_monitor_metric_rule_channels",
	} {
		if err := m.db.Exec("DROP TABLE IF EXISTS " + table).Error; err != nil {
			return fmt.Errorf("drop retired table %s: %w", table, err)
		}
	}
	return nil
}

func (m *Store) Close() error {
	if m == nil || m.db == nil {
		return nil
	}
	sqlDB, err := m.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
