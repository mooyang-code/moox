package inputcache

import (
	"context"
	"database/sql/driver"
	"fmt"
	"strings"
)

// DeleteKeys invalidates complete primary keys in schema key order. Callers
// must also invalidate their coverage proof and fence concurrent stale fills.
func (d *Database) DeleteKeys(ctx context.Context, keys [][]any) error {
	types := make(map[string]string, len(d.columns))
	for _, column := range d.columns {
		types[column.Name] = column.Type
	}
	normalized := make([][]any, len(keys))
	for i, key := range keys {
		if len(key) != len(d.keys) {
			return fmt.Errorf("cache deletion key %d has %d columns, expected %d", i, len(key), len(d.keys))
		}
		normalized[i] = make([]any, len(key))
		for j, raw := range key {
			value, err := driver.DefaultParameterConverter.ConvertValue(cacheParameter(types[d.keys[j]], raw))
			if err != nil {
				return fmt.Errorf("invalid cache deletion key %d: %w", i, err)
			}
			bytes, isBytes := value.([]byte)
			if value == nil || (isBytes && bytes == nil) {
				return fmt.Errorf("cache deletion key %d contains NULL", i)
			}
			normalized[i][j] = value
		}
	}
	if len(keys) == 0 {
		return nil
	}
	conditions := make([]string, len(d.keys))
	for i, key := range d.keys {
		conditions[i] = quoteIdentifier(key) + " = ?"
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, "DELETE FROM cached_rows WHERE "+strings.Join(conditions, " AND "))
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, key := range normalized {
		if _, err := stmt.ExecContext(ctx, key...); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	d.mutations.Add(1)
	return nil
}
