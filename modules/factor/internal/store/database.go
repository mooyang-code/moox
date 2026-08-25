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
	if err := s.migrateLegacyBindingSchema(); err != nil {
		return err
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

// migrateLegacyBindingSchema upgrades the immediately preceding factor
// binding shape in place. Older installations used dataset IDs directly;
// those IDs are retained as migration hints, but enabled legacy bindings are
// disabled because SQLite cannot infer the corresponding View IDs. The user
// can rebind them after metadata sync. Unrelated obsolete schemas still fail
// closed in validateSchemaTables.
func (s *Store) migrateLegacyBindingSchema() error {
	var table string
	if err := s.db.Raw("SELECT name FROM sqlite_master WHERE type = 'table' AND name = 't_factor_bindings'").Scan(&table).Error; err != nil {
		return fmt.Errorf("inspect factor binding schema: %w", err)
	}
	if table == "" {
		return nil
	}
	columns, err := s.tableColumns("t_factor_bindings")
	if err != nil {
		return err
	}
	if !containsColumn(columns, "c_source_dataset") || !containsColumn(columns, "c_target_dataset") {
		return nil
	}
	var enabled int64
	if err := s.db.Raw("SELECT COUNT(1) FROM t_factor_bindings WHERE c_status = 'enabled'").Scan(&enabled).Error; err != nil {
		return err
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		const create = `CREATE TABLE t_factor_bindings_migrating (
			c_binding_id TEXT NOT NULL PRIMARY KEY,
			c_factor_id TEXT NOT NULL,
			c_space_id TEXT NOT NULL,
			c_source_view_id TEXT NOT NULL,
			c_freq TEXT NOT NULL,
			c_subject_mode TEXT NOT NULL DEFAULT 'all',
			c_subjects_json TEXT NOT NULL DEFAULT '[]',
			c_result_dataset_id TEXT NOT NULL,
			c_result_view_id TEXT NOT NULL,
			c_status TEXT NOT NULL DEFAULT 'pending_view',
			c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			CHECK (c_subject_mode IN ('all', 'include')),
			CHECK (c_status IN ('pending_view', 'enabled', 'disabled', 'cleanup_pending')),
			FOREIGN KEY (c_factor_id) REFERENCES t_factor_defs (c_factor_id),
			UNIQUE (c_factor_id, c_space_id, c_source_view_id, c_freq)
		)`
		if err := tx.Exec(create).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO t_factor_bindings_migrating
			(c_binding_id, c_factor_id, c_space_id, c_source_view_id, c_freq, c_subject_mode, c_subjects_json, c_result_dataset_id, c_result_view_id, c_status, c_ctime, c_mtime)
			SELECT c_binding_id, c_factor_id, c_space_id, c_source_dataset, c_freq, c_subject_mode, c_subjects_json, c_target_dataset, c_target_dataset,
			CASE WHEN c_status = 'enabled' THEN 'disabled' ELSE c_status END, c_ctime, c_mtime
			FROM t_factor_bindings`).Error; err != nil {
			return err
		}
		if err := tx.Exec("DROP TABLE t_factor_bindings").Error; err != nil {
			return err
		}
		if err := tx.Exec("ALTER TABLE t_factor_bindings_migrating RENAME TO t_factor_bindings").Error; err != nil {
			return err
		}
		if err := tx.Exec(`CREATE TABLE t_factor_output_manifests (
			c_binding_id TEXT NOT NULL, c_subject_id TEXT NOT NULL, c_frequency TEXT NOT NULL,
			c_period_time INTEGER NOT NULL, c_row_keys_json TEXT NOT NULL DEFAULT '[]',
			c_updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (c_binding_id, c_subject_id, c_frequency, c_period_time),
			FOREIGN KEY (c_binding_id) REFERENCES t_factor_bindings(c_binding_id) ON DELETE CASCADE
		)`).Error; err != nil {
			return err
		}
		return nil
	})
	if err == nil && enabled > 0 {
		log.Warnf("factor schema migrated %d legacy enabled binding(s) to disabled; rebind them to explicit Source/Result Views", enabled)
	}
	return err
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
		"t_factor_defs": {
			"c_factor_id", "c_name", "c_source_code", "c_source_hash", "c_source_path",
			"c_input_columns_json", "c_outputs_json", "c_params_json", "c_lookback_periods",
			"c_status", "c_ctime", "c_mtime",
		},
		"t_factor_bindings": {
			"c_binding_id", "c_factor_id", "c_space_id", "c_source_view_id", "c_freq",
			"c_subject_mode", "c_subjects_json", "c_result_dataset_id", "c_result_view_id", "c_status", "c_ctime", "c_mtime",
		},
		"t_factor_output_manifests": {
			"c_binding_id", "c_subject_id", "c_frequency", "c_period_time", "c_row_keys_json", "c_updated_at",
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
