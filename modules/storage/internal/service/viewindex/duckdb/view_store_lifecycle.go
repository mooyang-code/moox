//go:build cgo

package duckdb

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"

	_ "github.com/marcboeker/go-duckdb/v2"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	trpc "trpc.group/trpc-go/trpc-go"
)

// Options 保存 DuckDB 视图存储打开配置。

func Open(opts Options) (*ViewStore, error) {
	if opts.Path == "" {
		return nil, errors.New("duckdb path is required")
	}
	db, err := sql.Open("duckdb", duckDBDSN(opts.Path))
	if err != nil {
		return nil, err
	}
	maxOpenConns := opts.MaxOpenConns
	if maxOpenConns <= 0 {
		maxOpenConns = defaultMaxOpenConns
	}
	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxOpenConns)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	store := &ViewStore{db: db}
	if err := store.init(trpc.BackgroundContext()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func duckDBDSN(path string) string {
	return appendDuckDBParams(path, map[string]string{
		duckDBMemoryLimitParam:          duckDBConfigValue(duckDBMemoryLimitEnv, defaultDuckDBMemoryLimit),
		duckDBThreadsParam:              duckDBConfigValue(duckDBThreadsEnv, defaultDuckDBThreads),
		duckDBMaxTempDirectorySizeParam: duckDBConfigValue(duckDBMaxTempDirectorySizeEnv, defaultDuckDBMaxTempDirSize),
	})
}

func duckDBConfigValue(envKey string, defaultValue string) string {
	if value := strings.TrimSpace(os.Getenv(envKey)); value != "" {
		return value
	}
	return defaultValue
}

func duckDBDSNHasParam(dsn string, name string) bool {
	queryStart := strings.Index(dsn, "?")
	if queryStart < 0 {
		return false
	}
	for _, part := range strings.Split(dsn[queryStart+1:], "&") {
		key, _, _ := strings.Cut(part, "=")
		if key == name {
			return true
		}
	}
	return false
}

func (s *ViewStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *ViewStore) Engine() string {
	return "duckdb"
}

func (s *ViewStore) Stat(ctx context.Context, indexID string) (viewindex.ViewIndexStats, error) {
	exists, err := s.tableExists(ctx, indexID)
	if err != nil || !exists {
		return viewindex.ViewIndexStats{Exists: exists}, err
	}
	stats, err := s.indexMeta(ctx, indexID)
	if err != nil {
		return viewindex.ViewIndexStats{}, err
	}
	return viewindex.ViewIndexStats{
		Exists:      true,
		ViewVersion: stats.viewVersion,
		EntryCount:  stats.entryCount,
		MinVersion:  stats.minVersion,
		MaxVersion:  stats.maxVersion,
		SchemaHash:  stats.schemaHash,
		UpdatedAt:   stats.updatedAt,
		IndexedFrom: stats.indexedFrom, IndexedTo: stats.indexedTo, ShardCheckpoints: stats.checkpoints,
	}, nil
}

func (s *ViewStore) Remove(ctx context.Context, indexID string) error {
	return s.DropResultTable(ctx, indexID)
}

func (s *ViewStore) init(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS moox_view_columns (
			table_name VARCHAR PRIMARY KEY,
			columns_json VARCHAR NOT NULL
		);
		CREATE TABLE IF NOT EXISTS moox_view_index_meta (
			table_name VARCHAR PRIMARY KEY,
			view_version UBIGINT NOT NULL DEFAULT 0,
			entry_count BIGINT NOT NULL DEFAULT 0,
			min_version VARCHAR NOT NULL DEFAULT '',
			max_version VARCHAR NOT NULL DEFAULT '',
			 schema_hash VARCHAR NOT NULL DEFAULT '',
			 indexed_from VARCHAR NOT NULL DEFAULT '',
			 indexed_to VARCHAR NOT NULL DEFAULT '',
			 checkpoints_json VARCHAR NOT NULL DEFAULT '{}',
			 updated_at VARCHAR NOT NULL DEFAULT ''
		);
		ALTER TABLE moox_view_index_meta ADD COLUMN IF NOT EXISTS view_version UBIGINT DEFAULT 0;
		ALTER TABLE moox_view_index_meta ADD COLUMN IF NOT EXISTS indexed_from VARCHAR DEFAULT '';
		ALTER TABLE moox_view_index_meta ADD COLUMN IF NOT EXISTS indexed_to VARCHAR DEFAULT '';
		ALTER TABLE moox_view_index_meta ADD COLUMN IF NOT EXISTS checkpoints_json VARCHAR DEFAULT '{}';
	`)
	return err
}
