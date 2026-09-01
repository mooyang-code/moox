package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	coremetadata "github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	_ "modernc.org/sqlite"
)

var _ coremetadata.Store = (*Store)(nil)

type readSnapshotContextKey struct{}

// queryDB keeps all metadata reads in one transaction while the cache is
// rebuilding. Normal requests continue to use the long-lived database handle.
func (s *Store) queryDB(ctx context.Context) readDB {
	if tx, ok := ctx.Value(readSnapshotContextKey{}).(*sql.Tx); ok {
		return tx
	}
	return s.db
}

// WithReadSnapshot executes a cache refresh against one SQLite read snapshot.
// It is intentionally optional so non-SQLite metadata readers keep working.
func (s *Store) WithReadSnapshot(ctx context.Context, fn func(context.Context) error) error {
	if s == nil || s.db == nil {
		return errors.New("metadata store is not open")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	readCtx := context.WithValue(ctx, readSnapshotContextKey{}, tx)
	if err := fn(readCtx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Options 保存 SQLite 元数据存储打开配置。
type Options struct {
	Path       string
	SchemaPath string
}

// Store 封装 SQLite 元数据表的直接读写能力。
type Store struct {
	db         *sql.DB
	schemaPath string
	now        func() time.Time
}

func Open(ctx context.Context, opts Options) (*Store, error) {
	if opts.Path == "" {
		return nil, errors.New("metadata sqlite path is required")
	}
	db, err := sql.Open("sqlite", withPragmas(opts.Path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode = WAL"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db, schemaPath: opts.SchemaPath, now: time.Now}, nil
}

func withPragmas(path string) string {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return path + separator + "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) InitSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("metadata store is not open")
	}
	if s.schemaPath == "" {
		return errors.New("metadata schema path is required for schema initialization")
	}
	if err := s.checkSchemaVersion(ctx); err != nil {
		return err
	}
	schema, err := os.ReadFile(s.schemaPath)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, string(schema))
	return err
}

// ValidateSchemaVersion checks that the persisted metadata database matches the
// schema shipped with the current binary without creating or altering tables.
func (s *Store) ValidateSchemaVersion(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("metadata store is not open")
	}
	if err := s.checkSchemaVersion(ctx); err != nil {
		return err
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = 't_schema_meta'`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return errors.New("storage metadata schema is not initialized")
	}
	return nil
}

const metadataSchemaVersion = "10"

func (s *Store) checkSchemaVersion(ctx context.Context) error {
	var schemaTableCount int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(1) FROM sqlite_master
		WHERE type = 'table' AND name = 't_schema_meta'
	`).Scan(&schemaTableCount); err != nil {
		return err
	}
	if schemaTableCount == 0 {
		var existingTables int
		if err := s.db.QueryRowContext(ctx, `
			SELECT COUNT(1) FROM sqlite_master
			WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
		`).Scan(&existingTables); err != nil {
			return err
		}
		if existingTables > 0 {
			return errors.New("incompatible storage metadata schema; reset metadata database")
		}
		return nil
	}
	var version string
	err := s.db.QueryRowContext(ctx, `SELECT c_value FROM t_schema_meta WHERE c_key = 'schema_version'`).Scan(&version)
	if err == nil && version == "5" {
		return errors.New("incompatible storage metadata schema v5; remove the metadata database and run init/import-seed")
	}
	if err == nil && version == "6" {
		if migrateErr := s.migrateV6ToV7(ctx); migrateErr != nil {
			return migrateErr
		}
		version = "7"
	}
	if err == nil && version == "7" {
		if migrateErr := s.migrateV7ToV8(ctx); migrateErr != nil {
			return migrateErr
		}
		version = "8"
	}
	if err == nil && version == "8" {
		if migrateErr := s.migrateV8ToV9(ctx); migrateErr != nil {
			return migrateErr
		}
		return nil
	}
	if err == nil && version == "9" {
		if migrateErr := s.migrateV9ToV10(ctx); migrateErr != nil {
			return migrateErr
		}
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("incompatible storage metadata schema; reset metadata database")
	}
	if err == nil && version != metadataSchemaVersion {
		return fmt.Errorf("incompatible storage metadata schema v%s; remove the metadata database and run init/import-seed", version)
	}
	return err
}

func (s *Store) migrateV9ToV10(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS t_dataset_subject_set_staging (
			c_space_id TEXT NOT NULL, c_set_id TEXT NOT NULL, c_dataset_id TEXT NOT NULL,
			c_subject_id TEXT NOT NULL, c_subject_role TEXT NOT NULL DEFAULT 'normal',
			c_effective_start_time DATETIME NOT NULL DEFAULT '', c_effective_end_time DATETIME NOT NULL DEFAULT '',
			c_status TEXT NOT NULL DEFAULT 'building', c_active_status TEXT NOT NULL DEFAULT 'active',
			c_attrs_json TEXT NOT NULL DEFAULT '{}', c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			CHECK (c_status IN ('building', 'activated')),
			CHECK (c_active_status IN ('active', 'disabled', 'building', 'archived', 'deleted')),
			CHECK (c_subject_role IN ('normal', 'benchmark', 'index', 'universe_member', 'record')),
			FOREIGN KEY (c_space_id, c_dataset_id) REFERENCES t_datasets (c_space_id, c_dataset_id) ON DELETE CASCADE ON UPDATE CASCADE,
			FOREIGN KEY (c_space_id, c_subject_id) REFERENCES t_subjects (c_space_id, c_subject_id) ON DELETE CASCADE ON UPDATE CASCADE,
			PRIMARY KEY (c_space_id, c_set_id, c_dataset_id, c_subject_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_t_dataset_subject_set_staging_set ON t_dataset_subject_set_staging (c_space_id, c_set_id)`,
		`UPDATE t_schema_meta SET c_value = '10' WHERE c_key = 'schema_version'`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate metadata schema v9 to v10: %w", err)
		}
	}
	return nil
}

// migrateV6ToV7 is the only additive metadata migration in this release. It
// preserves all existing catalog rows and creates the period/sync projections
// required by the View-ready event chain before advancing the version marker.
func (s *Store) migrateV6ToV7(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS t_view_period_dataset_states (
			c_space_id TEXT NOT NULL, c_view_id TEXT NOT NULL, c_dataset_id TEXT NOT NULL,
			c_frequency TEXT NOT NULL, c_period_time INTEGER NOT NULL, c_event_id TEXT NOT NULL,
			c_status TEXT NOT NULL CHECK (c_status IN ('complete', 'degraded')),
			c_subject_ids_json TEXT NOT NULL DEFAULT '[]', c_failed_subjects_json TEXT NOT NULL DEFAULT '[]',
			c_occurred_at TEXT NOT NULL, c_updated_at TEXT NOT NULL,
			PRIMARY KEY (c_space_id, c_view_id, c_dataset_id, c_frequency, c_period_time),
			FOREIGN KEY (c_space_id, c_view_id) REFERENCES t_views (c_space_id, c_view_id) ON DELETE CASCADE ON UPDATE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_t_view_period_dataset_states_period ON t_view_period_dataset_states (c_space_id, c_view_id, c_frequency, c_period_time)`,
		`CREATE TABLE IF NOT EXISTS t_view_sync_points (
			c_space_id TEXT NOT NULL, c_view_id TEXT NOT NULL, c_dataset_id TEXT NOT NULL,
			c_request_id TEXT NOT NULL, c_sync_point_id TEXT NOT NULL, c_applied_at TEXT NOT NULL,
			PRIMARY KEY (c_space_id, c_view_id, c_dataset_id, c_request_id),
			FOREIGN KEY (c_space_id, c_view_id) REFERENCES t_views (c_space_id, c_view_id) ON DELETE CASCADE ON UPDATE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_t_view_sync_points_request ON t_view_sync_points (c_space_id, c_view_id, c_request_id)`,
		`UPDATE t_schema_meta SET c_value = '7' WHERE c_key = 'schema_version'`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate metadata schema v6 to v7: %w", err)
		}
	}
	return nil
}

func (s *Store) migrateV7ToV8(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS t_view_rebuild_logs (
			c_log_id INTEGER PRIMARY KEY AUTOINCREMENT,
			c_space_id TEXT NOT NULL, c_view_id TEXT NOT NULL,
			c_build_id TEXT NOT NULL DEFAULT '', c_index_id TEXT NOT NULL DEFAULT '',
			c_trigger_reason INTEGER NOT NULL, c_result INTEGER NOT NULL,
			c_block_reason TEXT NOT NULL DEFAULT '', c_target_view_revision INTEGER NOT NULL DEFAULT 0,
			c_active_view_revision INTEGER NOT NULL DEFAULT 0, c_physical_bytes INTEGER NOT NULL DEFAULT 0,
			c_num_pending INTEGER NOT NULL DEFAULT 0, c_num_ack_pending INTEGER NOT NULL DEFAULT 0,
			c_entries_written INTEGER NOT NULL DEFAULT 0, c_started_at TEXT NOT NULL DEFAULT '',
			c_finished_at TEXT NOT NULL DEFAULT '', c_first_checked_at TEXT NOT NULL DEFAULT '',
			c_last_checked_at TEXT NOT NULL DEFAULT '', c_skip_count INTEGER NOT NULL DEFAULT 0,
			c_error_summary TEXT NOT NULL DEFAULT '', c_details_json TEXT NOT NULL DEFAULT '{}',
			c_created_at TEXT NOT NULL, c_updated_at TEXT NOT NULL,
			FOREIGN KEY (c_space_id, c_view_id) REFERENCES t_views(c_space_id, c_view_id) ON DELETE CASCADE ON UPDATE CASCADE,
			CHECK (c_result BETWEEN 1 AND 4), CHECK (c_trigger_reason BETWEEN 1 AND 9)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_t_view_rebuild_logs_view_time ON t_view_rebuild_logs (c_space_id, c_view_id, c_created_at DESC, c_log_id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_t_view_rebuild_logs_skip_key ON t_view_rebuild_logs (c_space_id, c_view_id, c_trigger_reason, c_result, c_block_reason)`,
		`UPDATE t_schema_meta SET c_value = '8' WHERE c_key = 'schema_version'`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate metadata schema v7 to v8: %w", err)
		}
	}
	return nil
}

// migrateV8ToV9 widens the rebuild-log trigger check to include the
// single-series capacity trigger. SQLite cannot alter a CHECK constraint, so
// rebuild the small audit table while preserving every existing row.
func (s *Store) migrateV8ToV9(ctx context.Context) error {
	var viewsTableCount int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = 't_views'`).Scan(&viewsTableCount); err != nil {
		return fmt.Errorf("migrate metadata schema v8 to v9: %w", err)
	}
	// A v7/v8 marker-only database is completed by the following InitSchema
	// call, which creates the table with the current constraint.
	if viewsTableCount == 0 {
		if _, err := s.db.ExecContext(ctx, `UPDATE t_schema_meta SET c_value = '9' WHERE c_key = 'schema_version'`); err != nil {
			return fmt.Errorf("migrate metadata schema v8 to v9: %w", err)
		}
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migrate metadata schema v8 to v9: %w", err)
	}
	rollback := func(cause error) error {
		_ = tx.Rollback()
		return fmt.Errorf("migrate metadata schema v8 to v9: %w", cause)
	}
	statements := []string{
		`ALTER TABLE t_view_rebuild_logs RENAME TO t_view_rebuild_logs_v8`,
		`DROP INDEX IF EXISTS idx_t_view_rebuild_logs_view_time`,
		`DROP INDEX IF EXISTS idx_t_view_rebuild_logs_skip_key`,
		`CREATE TABLE t_view_rebuild_logs (
			c_log_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			c_space_id TEXT NOT NULL, c_view_id TEXT NOT NULL,
			c_build_id TEXT NOT NULL DEFAULT '', c_index_id TEXT NOT NULL DEFAULT '',
			c_trigger_reason INTEGER NOT NULL, c_result INTEGER NOT NULL,
			c_block_reason TEXT NOT NULL DEFAULT '', c_target_view_revision INTEGER NOT NULL DEFAULT 0,
			c_active_view_revision INTEGER NOT NULL DEFAULT 0, c_physical_bytes INTEGER NOT NULL DEFAULT 0,
			c_num_pending INTEGER NOT NULL DEFAULT 0, c_num_ack_pending INTEGER NOT NULL DEFAULT 0,
			c_entries_written INTEGER NOT NULL DEFAULT 0, c_started_at TEXT NOT NULL DEFAULT '',
			c_finished_at TEXT NOT NULL DEFAULT '', c_first_checked_at TEXT NOT NULL DEFAULT '',
			c_last_checked_at TEXT NOT NULL DEFAULT '', c_skip_count INTEGER NOT NULL DEFAULT 0,
			c_error_summary TEXT NOT NULL DEFAULT '', c_details_json TEXT NOT NULL DEFAULT '{}',
			c_created_at TEXT NOT NULL, c_updated_at TEXT NOT NULL,
			FOREIGN KEY (c_space_id, c_view_id) REFERENCES t_views(c_space_id, c_view_id) ON DELETE CASCADE ON UPDATE CASCADE,
			CHECK (c_result BETWEEN 1 AND 4), CHECK (c_trigger_reason BETWEEN 1 AND 9)
		)`,
		`INSERT INTO t_view_rebuild_logs SELECT * FROM t_view_rebuild_logs_v8`,
		`DROP TABLE t_view_rebuild_logs_v8`,
		`CREATE INDEX idx_t_view_rebuild_logs_view_time ON t_view_rebuild_logs (c_space_id, c_view_id, c_created_at DESC, c_log_id DESC)`,
		`CREATE INDEX idx_t_view_rebuild_logs_skip_key ON t_view_rebuild_logs (c_space_id, c_view_id, c_trigger_reason, c_result, c_block_reason)`,
		`UPDATE t_schema_meta SET c_value = '9' WHERE c_key = 'schema_version'`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return rollback(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migrate metadata schema v8 to v9: %w", err)
	}
	return nil
}

func (s *Store) nowUTC() time.Time {
	if s != nil && s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

func (s *Store) TableNames(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT name
		FROM sqlite_master
		WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
		ORDER BY name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}
