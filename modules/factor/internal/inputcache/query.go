package inputcache

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

type WindowQuery struct {
	TimeColumn string
	Through    time.Time
	Lookback   int
	Filters    map[string][]any
}

// ReadWindow returns full source rows, never a completeness assertion. All
// non-time primary-key dimensions partition the lookback independently.
func (d *Database) ReadWindow(ctx context.Context, query WindowQuery) ([][]any, error) {
	if query.Through.IsZero() || query.Lookback <= 0 {
		return nil, fmt.Errorf("cache window requires target time and positive lookback")
	}
	known := make(map[string]string, len(d.columns))
	columns := make([]string, len(d.columns))
	for i, col := range d.columns {
		known[col.Name] = col.Type
		columns[i] = quoteIdentifier(col.Name)
	}
	if known[query.TimeColumn] != "TIMESTAMP_NS" {
		return nil, fmt.Errorf("cache window time column must be TIMESTAMP_NS")
	}
	partition := []string{}
	foundTime := false
	for _, key := range d.keys {
		if key == query.TimeColumn {
			foundTime = true
			continue
		}
		partition = append(partition, quoteIdentifier(key))
	}
	if !foundTime {
		return nil, fmt.Errorf("cache time column must be part of primary key")
	}
	conditions := []string{quoteIdentifier(query.TimeColumn) + " <= ?"}
	args := []any{query.Through.UTC()}
	names := make([]string, 0, len(query.Filters))
	for name := range query.Filters {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, ok := known[name]; !ok {
			return nil, fmt.Errorf("unknown cache filter column %q", name)
		}
		values := query.Filters[name]
		if len(values) == 0 {
			return nil, fmt.Errorf("empty cache filter for %q", name)
		}
		marks := make([]string, len(values))
		for i, value := range values {
			marks[i] = "?"
			args = append(args, value)
		}
		conditions = append(conditions, quoteIdentifier(name)+" IN ("+strings.Join(marks, ",")+")")
	}
	window := ""
	if len(partition) > 0 {
		window = "PARTITION BY " + strings.Join(partition, ",") + " "
	}
	window += "ORDER BY " + quoteIdentifier(query.TimeColumn) + " DESC"
	order := append(append([]string{}, partition...), quoteIdentifier(query.TimeColumn))
	statement := "SELECT " + strings.Join(columns, ",") + " FROM cached_rows WHERE " + strings.Join(conditions, " AND ") +
		" QUALIFY ROW_NUMBER() OVER (" + window + ") <= ? ORDER BY " + strings.Join(order, ",")
	args = append(args, query.Lookback)
	rows, err := d.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := [][]any{}
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		result = append(result, values)
	}
	return result, rows.Err()
}
