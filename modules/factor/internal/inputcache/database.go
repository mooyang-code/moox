package inputcache

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"
)

const updatedColumn = "__moox_cache_updated_at"

type Column struct {
	Name string
	Type string
}

// Database is one disposable, immutable-schema generation of a source View.
type Database struct {
	db        *sql.DB
	columns   []Column
	keys      []string
	mutations atomic.Uint64
}

func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func CreateDatabase(ctx context.Context, path string, columns []Column, keys []string) (*Database, error) {
	if len(columns) == 0 || len(keys) == 0 {
		return nil, fmt.Errorf("cache requires full source columns and primary key")
	}
	seen := map[string]bool{}
	defs := make([]string, 0, len(columns)+2)
	for _, col := range columns {
		name := strings.ToLower(col.Name)
		if name == "" || strings.ContainsRune(name, 0) || name == updatedColumn || seen[name] {
			return nil, fmt.Errorf("invalid or duplicate cache column %q", col.Name)
		}
		seen[name] = true
		switch col.Type {
		case "VARCHAR", "BIGINT", "DOUBLE", "BOOLEAN", "TIMESTAMP_NS", "BLOB", "UBIGINT", "JSON":
		default:
			return nil, fmt.Errorf("unsupported cache column type %q", col.Type)
		}
		defs = append(defs, quoteIdentifier(col.Name)+" "+col.Type)
	}
	quotedKeys := make([]string, len(keys))
	keySeen := map[string]bool{}
	for i, key := range keys {
		name := strings.ToLower(key)
		if !seen[name] || keySeen[name] {
			return nil, fmt.Errorf("invalid cache primary key %q", key)
		}
		keySeen[name] = true
		for _, column := range columns {
			if strings.EqualFold(column.Name, key) && column.Name != key {
				return nil, fmt.Errorf("cache primary key must use exact column name %q", column.Name)
			}
		}
		quotedKeys[i] = quoteIdentifier(key)
	}
	defs = append(defs, quoteIdentifier(updatedColumn)+" TIMESTAMP_NS NOT NULL", "PRIMARY KEY ("+strings.Join(quotedKeys, ",")+")")
	db, err := sql.Open("duckdb", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, "CREATE TABLE cached_rows ("+strings.Join(defs, ",")+")"); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Database{db: db, columns: append([]Column(nil), columns...), keys: append([]string(nil), keys...)}, nil
}

func (d *Database) Close() error { return d.db.Close() }

// Upsert accepts complete source rows only; partial projections cannot populate
// this cache because an absent field must not be mistaken for a source NULL.
func (d *Database) Upsert(ctx context.Context, rows [][]any, updatedAt time.Time) error {
	if updatedAt.IsZero() {
		return fmt.Errorf("cache update time is required")
	}
	for i, row := range rows {
		if len(row) != len(d.columns) {
			return fmt.Errorf("cache row %d has %d columns, expected %d", i, len(row), len(d.columns))
		}
	}
	if len(rows) == 0 {
		return nil
	}
	names := make([]string, 0, len(d.columns)+1)
	marks := make([]string, 0, len(d.columns)+1)
	for _, col := range d.columns {
		names = append(names, quoteIdentifier(col.Name))
		marks = append(marks, "?")
	}
	names = append(names, quoteIdentifier(updatedColumn))
	marks = append(marks, "?")
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, "INSERT OR REPLACE INTO cached_rows ("+strings.Join(names, ",")+") VALUES ("+strings.Join(marks, ",")+")")
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, row := range rows {
		values := append(append([]any(nil), row...), updatedAt.UTC())
		for i, column := range d.columns {
			values[i] = cacheParameter(column.Type, values[i])
		}
		if _, err := stmt.ExecContext(ctx, values...); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	d.mutations.Add(1)
	return nil
}
