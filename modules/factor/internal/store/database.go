// Package store owns Factor's SQLite connection and persistence repositories.
package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// IsDatabaseCorruption reports SQLite errors that indicate the local factor
// database can no longer be trusted for writes. The pure-Go SQLite driver wraps
// these errors, so matching the stable SQLite message is more reliable than
// depending on a driver-specific error type.
func IsDatabaseCorruption(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"database disk image is malformed",
		"malformed database schema",
		"database corruption",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// Store owns the Factor SQLite connection and repositories.
type Store struct {
	db        *gorm.DB
	factors   *FactorRepository
	bindings  *BindingRepository
	manifests *OutputManifestRepository
}

// Options configures the Factor SQLite store.
type Options struct {
	Path            string
	MaxIdleConns    int
	MaxOpenConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

// Open opens the Factor SQLite store. Schema creation is handled by bootstrap.
func Open(opts *Options) (*Store, error) {
	dbPath := "./data/factor/factor.db"
	if opts != nil && opts.Path != "" {
		dbPath = opts.Path
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	db, err := gorm.Open(sqlite.Open(buildSQLiteDSN(dbPath)), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	s := &Store{db: db}
	s.factors = NewFactorRepository(db)
	s.bindings = NewBindingRepository(db)
	s.manifests = NewOutputManifestRepository(db)
	applySQLitePoolConfig(db, opts)
	log.Infof("初始化 Factor SQLite 数据库: %s", dbPath)
	return s, nil
}

// Factors returns the factor repository.
func (s *Store) Factors() *FactorRepository {
	if s == nil {
		return nil
	}
	return s.factors
}

// Bindings returns the factor binding repository.
func (s *Store) Bindings() *BindingRepository {
	if s == nil {
		return nil
	}
	return s.bindings
}

// OutputManifests returns the dynamic output manifest repository.
func (s *Store) OutputManifests() *OutputManifestRepository {
	if s == nil {
		return nil
	}
	return s.manifests
}

// ApplySchema applies schema SQL during service startup.
func (s *Store) ApplySchema(sql string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("factor database is not open")
	}
	if strings.TrimSpace(sql) == "" {
		return fmt.Errorf("factor schema sql is empty")
	}
	tables, err := s.factorSchemaTables()
	if err != nil {
		return err
	}
	if len(tables) > 0 {
		if err := s.validateSchemaTables(tables); err != nil {
			return err
		}
	}
	if err := s.db.Exec(sql).Error; err != nil {
		return err
	}
	return s.validateSchema()
}

func (s *Store) tableColumns(table string) ([]string, error) {
	var columns []string
	if err := s.db.Raw("SELECT name FROM pragma_table_info(?) ORDER BY cid", table).Scan(&columns).Error; err != nil {
		return nil, fmt.Errorf("inspect factor schema table %s: %w", table, err)
	}
	return columns, nil
}

func containsColumn(columns []string, expected string) bool {
	for _, column := range columns {
		if column == expected {
			return true
		}
	}
	return false
}

func (s *Store) validateSchema() error {
	tables, err := s.factorSchemaTables()
	if err != nil {
		return err
	}
	return s.validateSchemaTables(tables)
}

func (s *Store) factorSchemaTables() ([]string, error) {
	var tables []string
	if err := s.db.Raw(
		"SELECT name FROM sqlite_master WHERE type = 'table' AND name LIKE 't_factor_%' ORDER BY name",
	).Scan(&tables).Error; err != nil {
		return nil, fmt.Errorf("inspect factor schema tables: %w", err)
	}
	return tables, nil
}

func (s *Store) validateSchemaTables(tables []string) error {
	expected := map[string][]string{
		"t_factor_subject_receipts": {"c_space_id", "c_event_id", "c_catalog_revision", "c_period_time", "c_source_view_id", "c_event_json", "c_outcomes_json", "c_status", "c_updated_at"},
		"t_factor_subject_runs":     {"c_task_id", "c_scope_key", "c_period_time", "c_task_json", "c_status", "c_error", "c_updated_at"},
		"t_factor_subject_heads":    {"c_scope_key", "c_task_id", "c_period_time", "c_source_node", "c_source_store", "c_source_sequence", "c_source_event", "c_catalog_revision"},
		"t_factor_subject_gc":       {"c_id", "c_completed_before"},
		"t_factor_catalog":          {"c_id", "c_revision", "c_snapshot_hash"},
		"t_factor_defs": {
			"c_factor_id", "c_name", "c_factor_type", "c_source_code", "c_source_hash", "c_source_path",
			"c_input_columns_json", "c_outputs_json", "c_params_json", "c_lookback_periods",
			"c_status", "c_ctime", "c_mtime",
		},
		"t_factor_bindings": {
			"c_binding_id", "c_binding_generation", "c_factor_id", "c_space_id", "c_source_view_id", "c_freq",
			"c_subject_mode", "c_subjects_json", "c_result_dataset_id", "c_result_view_id", "c_status", "c_ctime", "c_mtime",
		},
		"t_factor_output_manifests": {
			"c_source_series_tag", "c_filter_source_series_tag", "c_binding_id", "c_binding_generation", "c_cleanup_task_json", "c_subject_id", "c_frequency", "c_period_time", "c_row_keys_json", "c_updated_at",
		},
	}
	if len(tables) != len(expected) {
		return fmt.Errorf("factor database uses an obsolete schema; create a fresh database")
	}
	for _, table := range tables {
		want, ok := expected[table]
		if !ok {
			return fmt.Errorf("factor database uses an obsolete schema; create a fresh database")
		}
		var columns []string
		if err := s.db.Raw("SELECT name FROM pragma_table_info(?) ORDER BY cid", table).Scan(&columns).Error; err != nil {
			return fmt.Errorf("inspect factor schema table %s: %w", table, err)
		}
		if strings.Join(columns, "\x00") != strings.Join(want, "\x00") {
			return fmt.Errorf("factor database table %s uses an obsolete schema; create a fresh database", table)
		}
	}
	return nil
}

// Ping verifies that the database is available.
func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("factor database is not open")
	}
	return s.db.WithContext(ctx).Exec("SELECT 1").Error
}

// Close releases the underlying SQL connection.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

func buildSQLiteDSN(dbPath string) string {
	pragmas := []string{
		"_pragma=foreign_keys(ON)",
		"_pragma=journal_mode(WAL)",
		"_pragma=synchronous(NORMAL)",
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

func applySQLitePoolConfig(db *gorm.DB, cfg *Options) {
	sqlDB, err := db.DB()
	if err != nil {
		return
	}
	// Factor writes output manifests from many task workers. Keep a single
	// SQLite connection so concurrent WAL writes cannot corrupt the catalog.
	maxOpen := 1
	maxIdle := 1
	if cfg != nil {
		if cfg.MaxOpenConns > 0 {
			maxOpen = cfg.MaxOpenConns
		}
		if cfg.MaxIdleConns > 0 && cfg.MaxIdleConns < maxOpen {
			maxIdle = cfg.MaxIdleConns
		}
		if cfg.ConnMaxLifetime > 0 {
			sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)
		}
		if cfg.ConnMaxIdleTime > 0 {
			sqlDB.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)
		}
	}
	sqlDB.SetMaxOpenConns(maxOpen)
	sqlDB.SetMaxIdleConns(maxIdle)
}
