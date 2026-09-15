package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
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

const metadataSchemaVersion = "11"

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
		version = "10"
	}
	if err == nil && version == "10" {
		if migrateErr := s.migrateV10ToV11(ctx); migrateErr != nil {
			return migrateErr
		}
		version = "11"
	}
	if err == nil && version == metadataSchemaVersion {
		if repairErr := s.repairViewChildForeignKeys(ctx); repairErr != nil {
			return repairErr
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

// migrateV10ToV11 collapses View membership to a single dataset_id. Production
// v10 rows still store c_primary_dataset_id plus c_dataset_ids_json; the new
// catalog rejects multi-dataset Views, so a join list other than the primary
// is a hard stop instead of a silent truncation.
func (s *Store) migrateV10ToV11(ctx context.Context) error {
	var viewsTableCount int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = 't_views'`).Scan(&viewsTableCount); err != nil {
		return fmt.Errorf("migrate metadata schema v10 to v11: %w", err)
	}
	if viewsTableCount == 0 {
		if _, err := s.db.ExecContext(ctx, `UPDATE t_schema_meta SET c_value = '11' WHERE c_key = 'schema_version'`); err != nil {
			return fmt.Errorf("migrate metadata schema v10 to v11: %w", err)
		}
		return nil
	}
	columns := map[string]bool{}
	colRows, err := s.db.QueryContext(ctx, `SELECT name FROM pragma_table_info('t_views')`)
	if err != nil {
		return fmt.Errorf("migrate metadata schema v10 to v11: %w", err)
	}
	for colRows.Next() {
		var name string
		if err := colRows.Scan(&name); err != nil {
			colRows.Close()
			return fmt.Errorf("migrate metadata schema v10 to v11: %w", err)
		}
		columns[name] = true
	}
	if err := colRows.Err(); err != nil {
		colRows.Close()
		return fmt.Errorf("migrate metadata schema v10 to v11: %w", err)
	}
	colRows.Close()
	if columns["c_dataset_id"] && !columns["c_primary_dataset_id"] {
		if _, err := s.db.ExecContext(ctx, `UPDATE t_schema_meta SET c_value = '11' WHERE c_key = 'schema_version'`); err != nil {
			return fmt.Errorf("migrate metadata schema v10 to v11: %w", err)
		}
		return nil
	}
	if !columns["c_primary_dataset_id"] {
		return fmt.Errorf("migrate metadata schema v10 to v11: t_views is missing c_primary_dataset_id")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT c_space_id, c_view_id, c_primary_dataset_id, c_dataset_ids_json FROM t_views`)
	if err != nil {
		return fmt.Errorf("migrate metadata schema v10 to v11: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var spaceID, viewID, primaryID, rawIDs string
		if err := rows.Scan(&spaceID, &viewID, &primaryID, &rawIDs); err != nil {
			return fmt.Errorf("migrate metadata schema v10 to v11: %w", err)
		}
		if err := validateV10SingleDatasetView(spaceID, viewID, primaryID, rawIDs); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("migrate metadata schema v10 to v11: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("migrate metadata schema v10 to v11: %w", err)
	}
	defer func() { _, _ = s.db.ExecContext(ctx, `PRAGMA foreign_keys = ON`) }()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migrate metadata schema v10 to v11: %w", err)
	}
	rollback := func(cause error) error {
		_ = tx.Rollback()
		return fmt.Errorf("migrate metadata schema v10 to v11: %w", cause)
	}
	statements := []string{
		`ALTER TABLE t_views RENAME TO t_views_v10`,
		`CREATE TABLE t_views (
			c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			c_space_id TEXT NOT NULL,
			c_view_id TEXT NOT NULL,
			c_name TEXT NOT NULL,
			c_description TEXT NOT NULL DEFAULT '',
			c_dataset_id TEXT NOT NULL,
			c_grain_keys_json TEXT NOT NULL DEFAULT '[]',
			c_filter_json TEXT NOT NULL DEFAULT '{}',
			c_engine TEXT NOT NULL DEFAULT 'duckdb',
			c_keep_duration TEXT NOT NULL DEFAULT '0',
			c_active_index_id TEXT NOT NULL DEFAULT '',
			c_desired_view_revision INTEGER NOT NULL DEFAULT 1,
			c_active_view_revision INTEGER NOT NULL DEFAULT 0,
			c_active_columns_json TEXT NOT NULL DEFAULT '[]',
			c_active_view_schema_hash TEXT NOT NULL DEFAULT '',
			c_active_slot TEXT NOT NULL DEFAULT 'slot-a',
			c_indexed_from TEXT NOT NULL DEFAULT '',
			c_indexed_to TEXT NOT NULL DEFAULT '',
			c_status TEXT NOT NULL DEFAULT 'active',
			c_attrs_json TEXT NOT NULL DEFAULT '{}',
			c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			CHECK (c_engine IN ('duckdb', 'bleve')),
			CHECK (c_status IN ('active', 'disabled', 'building', 'archived', 'deleted')),
			FOREIGN KEY (c_space_id) REFERENCES t_spaces (c_space_id) ON DELETE CASCADE ON UPDATE CASCADE,
			FOREIGN KEY (c_space_id, c_dataset_id) REFERENCES t_datasets (c_space_id, c_dataset_id) ON DELETE RESTRICT ON UPDATE CASCADE,
			UNIQUE (c_space_id, c_view_id),
			UNIQUE (c_space_id, c_name)
		)`,
		`INSERT INTO t_views (
			c_id, c_space_id, c_view_id, c_name, c_description, c_dataset_id, c_grain_keys_json,
			c_filter_json, c_engine, c_keep_duration, c_active_index_id, c_desired_view_revision,
			c_active_view_revision, c_active_columns_json, c_active_view_schema_hash, c_active_slot,
			c_indexed_from, c_indexed_to, c_status, c_attrs_json, c_ctime, c_mtime
		) SELECT
			c_id, c_space_id, c_view_id, c_name, c_description, c_primary_dataset_id, c_grain_keys_json,
			c_filter_json, c_engine, c_keep_duration, c_active_index_id, c_desired_view_revision,
			c_active_view_revision, c_active_columns_json, c_active_view_schema_hash, c_active_slot,
			c_indexed_from, c_indexed_to, c_status, c_attrs_json, c_ctime, c_mtime
		FROM t_views_v10`,
		`DROP TABLE t_views_v10`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return rollback(fmt.Errorf("%w: %s", err, compactSQL(statement)))
		}
	}
	if err := rebuildViewChildForeignKeys(ctx, tx); err != nil {
		return rollback(err)
	}
	statements = []string{
		`CREATE INDEX idx_t_views_space ON t_views (c_space_id, c_status)`,
		`CREATE INDEX idx_t_views_dataset ON t_views (c_space_id, c_dataset_id, c_status)`,
		`CREATE INDEX idx_t_views_revision_pending ON t_views (c_space_id, c_status, c_desired_view_revision, c_active_view_revision)`,
		`CREATE TRIGGER trg_t_views_mtime
AFTER UPDATE ON t_views
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
	UPDATE t_views SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END`,
		`UPDATE t_schema_meta SET c_value = '11' WHERE c_key = 'schema_version'`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return rollback(fmt.Errorf("%w: %s", err, compactSQL(statement)))
		}
	}
	if err := rewriteMigratedViewAttrs(ctx, tx); err != nil {
		return rollback(err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migrate metadata schema v10 to v11: %w", err)
	}
	return nil
}

func validateV10SingleDatasetView(spaceID, viewID, primaryID, rawIDs string) error {
	primaryID = strings.TrimSpace(primaryID)
	if primaryID == "" {
		return fmt.Errorf("incompatible storage metadata schema v10: view %s/%s is not a single dataset", spaceID, viewID)
	}
	var ids []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(rawIDs)), &ids); err != nil {
		return fmt.Errorf("incompatible storage metadata schema v10: view %s/%s is not a single dataset", spaceID, viewID)
	}
	if len(ids) == 0 {
		ids = []string{primaryID}
	}
	if len(ids) != 1 || strings.TrimSpace(ids[0]) != primaryID {
		return fmt.Errorf("incompatible storage metadata schema v10: view %s/%s is not a single dataset", spaceID, viewID)
	}
	return nil
}

func rewriteMigratedViewAttrs(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT c_space_id, c_view_id, c_dataset_id, c_attrs_json FROM t_views`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type viewAttr struct {
		spaceID, viewID, datasetID, raw string
	}
	var items []viewAttr
	for rows.Next() {
		var item viewAttr
		if err := rows.Scan(&item.spaceID, &item.viewID, &item.datasetID, &item.raw); err != nil {
			return err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, item := range items {
		next, err := rewriteV10ViewAttrsJSON(item.datasetID, item.raw)
		if err != nil {
			return fmt.Errorf("view %s/%s: %w", item.spaceID, item.viewID, err)
		}
		if next == item.raw {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE t_views SET c_attrs_json = ? WHERE c_space_id = ? AND c_view_id = ?`, next, item.spaceID, item.viewID); err != nil {
			return err
		}
	}
	return nil
}

func rewriteV10ViewAttrsJSON(datasetID, raw string) (string, error) {
	datasetID = strings.TrimSpace(datasetID)
	raw = strings.TrimSpace(raw)
	if datasetID == "" {
		return "", errors.New("dataset_id is required")
	}
	if raw == "" {
		raw = "{}"
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return "", err
	}
	obj["dataset_id"] = datasetID
	delete(obj, "primary_dataset_id")
	delete(obj, "dataset_ids")
	attrs, _ := obj["attributes"].(map[string]any)
	if attrs == nil {
		attrs = map[string]any{}
		obj["attributes"] = attrs
	}
	attrs["moox.active_dataset_id"] = datasetID
	if strings.TrimSpace(fmt.Sprint(attrs["moox.active_primary_dataset_id"])) == "" || attrs["moox.active_primary_dataset_id"] == nil {
		attrs["moox.active_primary_dataset_id"] = datasetID
	}
	data, err := json.Marshal(obj)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

var createTableNamePattern = regexp.MustCompile(`(?i)^(CREATE TABLE(?:\s+IF NOT EXISTS)?)\s+(?:main\.)?(?:"([^"]+)"|'([^']+)'|([A-Za-z_][A-Za-z0-9_]*))`)

func compactSQL(statement string) string {
	return strings.Join(strings.Fields(statement), " ")
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func rewriteCreateTableName(createSQL, tableName, tempName string) (string, error) {
	sql := strings.ReplaceAll(createSQL, "t_views_v10", "t_views")
	matches := createTableNamePattern.FindStringSubmatchIndex(sql)
	if matches == nil {
		return "", fmt.Errorf("rebuild %s: invalid create table sql", tableName)
	}
	nameStart, nameEnd := -1, -1
	found := ""
	for i := 2; i <= 4; i++ {
		start := matches[i*2]
		end := matches[i*2+1]
		if start < 0 {
			continue
		}
		nameStart, nameEnd = start, end
		found = sql[start:end]
		break
	}
	if found != tableName {
		return "", fmt.Errorf("rebuild %s: create table sql names %q", tableName, found)
	}
	return sql[:nameStart] + tempName + sql[nameEnd:], nil
}

func leftoverViewV10Objects(ctx context.Context, queryRow func(context.Context, string, ...any) *sql.Row) (int, error) {
	var leftover int
	if err := queryRow(ctx, `SELECT COUNT(1) FROM sqlite_master WHERE sql LIKE '%t_views_v10%'`).Scan(&leftover); err != nil {
		return 0, err
	}
	return leftover, nil
}

func (s *Store) repairViewChildForeignKeys(ctx context.Context) error {
	leftover, err := leftoverViewV10Objects(ctx, s.db.QueryRowContext)
	if err != nil {
		return fmt.Errorf("repair view child foreign keys: %w", err)
	}
	if leftover == 0 {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("repair view child foreign keys: %w", err)
	}
	defer func() { _, _ = s.db.ExecContext(ctx, `PRAGMA foreign_keys = ON`) }()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("repair view child foreign keys: %w", err)
	}
	if err := rebuildViewChildForeignKeys(ctx, tx); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("repair view child foreign keys: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("repair view child foreign keys: %w", err)
	}
	return nil
}

func rebuildViewChildForeignKeys(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT name, sql
		FROM sqlite_master
		WHERE type = 'table' AND sql LIKE '%t_views_v10%'
		ORDER BY name
	`)
	if err != nil {
		return err
	}
	type tableSQL struct {
		name string
		sql  string
	}
	var tables []tableSQL
	for rows.Next() {
		var item tableSQL
		if err := rows.Scan(&item.name, &item.sql); err != nil {
			rows.Close()
			return err
		}
		tables = append(tables, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, table := range tables {
		if err := rebuildOneViewChildTable(ctx, tx, table.name, table.sql); err != nil {
			return err
		}
	}
	leftover, err := leftoverViewV10Objects(ctx, tx.QueryRowContext)
	if err != nil {
		return err
	}
	if leftover != 0 {
		return fmt.Errorf("%d sqlite objects still reference t_views_v10", leftover)
	}
	return nil
}

func rebuildOneViewChildTable(ctx context.Context, tx *sql.Tx, tableName, createSQL string) error {
	objectRows, err := tx.QueryContext(ctx, `
		SELECT sql
		FROM sqlite_master
		WHERE tbl_name = ? AND type IN ('index', 'trigger') AND sql IS NOT NULL
		ORDER BY type, name
	`, tableName)
	if err != nil {
		return err
	}
	var extras []string
	for objectRows.Next() {
		var sqlText string
		if err := objectRows.Scan(&sqlText); err != nil {
			objectRows.Close()
			return err
		}
		extras = append(extras, strings.ReplaceAll(sqlText, "t_views_v10", "t_views"))
	}
	if err := objectRows.Err(); err != nil {
		objectRows.Close()
		return err
	}
	objectRows.Close()

	tempName := tableName + "__fkfix"
	rewritten, err := rewriteCreateTableName(createSQL, tableName, tempName)
	if err != nil {
		return err
	}
	statements := []string{
		rewritten,
		`INSERT INTO ` + quoteIdent(tempName) + ` SELECT * FROM ` + quoteIdent(tableName),
		`DROP TABLE ` + quoteIdent(tableName),
		`ALTER TABLE ` + quoteIdent(tempName) + ` RENAME TO ` + quoteIdent(tableName),
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("%w: %s", err, compactSQL(statement))
		}
	}
	if err := restoreAutoincrement(ctx, tx, tableName); err != nil {
		return err
	}
	for _, statement := range extras {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("%w: %s", err, compactSQL(statement))
		}
	}
	return nil
}

func restoreAutoincrement(ctx context.Context, tx *sql.Tx, tableName string) error {
	var hasID int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM pragma_table_info(`+quoteIdent(tableName)+`) WHERE name = 'c_id' AND pk = 1`).Scan(&hasID); err != nil {
		return err
	}
	if hasID == 0 {
		return nil
	}
	var seqTable int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM sqlite_master WHERE name = 'sqlite_sequence'`).Scan(&seqTable); err != nil {
		return err
	}
	if seqTable == 0 {
		return nil
	}
	var maxID sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(c_id) FROM `+quoteIdent(tableName)).Scan(&maxID); err != nil {
		return err
	}
	if !maxID.Valid {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sqlite_sequence WHERE name = ?`, tableName); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO sqlite_sequence (name, seq) VALUES (?, ?)`, tableName, maxID.Int64)
	return err
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
