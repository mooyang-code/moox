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
	db       *gorm.DB
	factors  *FactorRepository
	bindings *BindingRepository
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
			"c_binding_id", "c_factor_id", "c_space_id", "c_source_dataset", "c_freq",
			"c_subject_mode", "c_subjects_json", "c_target_dataset", "c_status", "c_ctime", "c_mtime",
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
	maxOpen := 30
	maxIdle := 20
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
