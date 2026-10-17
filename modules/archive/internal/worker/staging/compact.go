package staging

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
)

// Sizes 是库文件的大小与存活数据量。
type Sizes struct {
	FileBytes int64
	UsedBytes int64
}

// Sizes 读取 PRAGMA database_size：文件总块数与已用块数。
func (s *Store) Sizes(ctx context.Context) (Sizes, error) {
	rows, err := s.db.QueryContext(ctx, `PRAGMA database_size`)
	if err != nil {
		return Sizes{}, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return Sizes{}, err
	}
	if !rows.Next() {
		return Sizes{}, fmt.Errorf("PRAGMA database_size returned no rows")
	}
	values := make([]any, len(columns))
	for i := range values {
		values[i] = new(sql.NullString)
	}
	if err := rows.Scan(values...); err != nil {
		return Sizes{}, err
	}
	get := func(name string) int64 {
		for i, column := range columns {
			if strings.EqualFold(column, name) {
				var n int64
				fmt.Sscan(values[i].(*sql.NullString).String, &n)
				return n
			}
		}
		return 0
	}
	block := get("block_size")
	return Sizes{FileBytes: get("total_blocks") * block, UsedBytes: get("used_blocks") * block}, nil
}

// Compact 在库文件明显大于存活数据时重建：DuckDB 删除数据后空间只在库内复用，文件不会自动变小。
// 文件超过存活数据的 ratio 倍且不小于 minBytes 时，把库复制到新文件（COPY FROM DATABASE）并替换。
// 调用方必须保证此时没有运行在使用暂存库。
func (s *Store) Compact(ctx context.Context, ratio float64, minBytes int64) (bool, error) {
	if err := s.Checkpoint(ctx); err != nil {
		return false, err
	}
	sizes, err := s.Sizes(ctx)
	if err != nil {
		return false, err
	}
	if sizes.FileBytes < minBytes || float64(sizes.FileBytes) <= ratio*float64(sizes.UsedBytes) {
		return false, nil
	}
	var catalog string
	if err := s.db.QueryRowContext(ctx, `SELECT current_database()`).Scan(&catalog); err != nil {
		return false, err
	}
	fresh := s.path + ".new"
	_ = os.Remove(fresh)
	_ = os.Remove(fresh + ".wal")
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`ATTACH %s AS fresh`, literal(fresh))); err != nil {
		return false, fmt.Errorf("attach new staging file: %w", err)
	}
	copyErr := func() error {
		if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`COPY FROM DATABASE %s TO fresh`, quote(catalog))); err != nil {
			return err
		}
		return nil
	}()
	if _, err := s.db.ExecContext(ctx, `DETACH fresh`); err != nil && copyErr == nil {
		copyErr = err
	}
	if copyErr != nil {
		_ = os.Remove(fresh)
		_ = os.Remove(fresh + ".wal")
		return false, fmt.Errorf("copy staging database: %w", copyErr)
	}
	if err := s.db.Close(); err != nil {
		return false, err
	}
	old := s.path + ".old"
	_ = os.Remove(old)
	swapErr := os.Rename(s.path, old)
	if swapErr == nil {
		if swapErr = os.Rename(fresh, s.path); swapErr != nil {
			_ = os.Rename(old, s.path) // 换不上就退回旧文件
		}
	}
	_ = os.Remove(s.path + ".wal")
	db, openErr := openDB(s.path, TempDir(s.path), s.opts)
	if openErr != nil {
		return false, fmt.Errorf("reopen staging database after compaction: %w", openErr)
	}
	s.db = db
	if swapErr != nil {
		return false, fmt.Errorf("replace staging file: %w", swapErr)
	}
	_ = os.Remove(old)
	_ = os.Remove(old + ".wal")
	return true, nil
}

// CleanTemp 清理临时目录里的残留文件（只在没有运行时调用）。
func (s *Store) CleanTemp() error {
	dir := TempDir(s.path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(dir + string(os.PathSeparator) + entry.Name()); err != nil {
			return err
		}
	}
	return nil
}
