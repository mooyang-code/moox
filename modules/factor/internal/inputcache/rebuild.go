package inputcache

import (
	"context"
	"fmt"
	"strings"
)

// Rebuild copies the most recently modified rows into a new schema-identical
// file. The caller owns generation switching; the source remains untouched.
func (d *Database) Rebuild(ctx context.Context, path string, keepRows int64) (_ *Database, err error) {
	if keepRows <= 0 {
		return nil, fmt.Errorf("cache rebuild row limit must be positive")
	}
	columns := make([]string, 0, len(d.columns)+1)
	readColumns := make([]string, 0, len(d.columns)+1)
	marks := make([]string, 0, len(d.columns)+1)
	for _, col := range d.columns {
		columns = append(columns, quoteIdentifier(col.Name))
		readColumns = append(readColumns, cacheReadColumn(col))
		marks = append(marks, "?")
	}
	columns = append(columns, quoteIdentifier(updatedColumn))
	readColumns = append(readColumns, quoteIdentifier(updatedColumn))
	marks = append(marks, "?")
	order := []string{quoteIdentifier(updatedColumn) + " DESC"}
	for _, key := range d.keys {
		order = append(order, quoteIdentifier(key))
	}
	rows, err := d.db.QueryContext(ctx, "SELECT "+strings.Join(readColumns, ",")+" FROM cached_rows ORDER BY "+strings.Join(order, ",")+" LIMIT ?", keepRows)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	next, err := CreateDatabase(ctx, path, d.columns, d.keys)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = next.Close()
		}
	}()
	tx, err := next.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, "INSERT INTO cached_rows ("+strings.Join(columns, ",")+") VALUES ("+strings.Join(marks, ",")+")")
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err = rows.Scan(dest...); err != nil {
			return nil, err
		}
		for i, column := range d.columns {
			values[i] = cacheParameter(column.Type, values[i])
		}
		if _, err = stmt.ExecContext(ctx, values...); err != nil {
			return nil, err
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return next, nil
}
