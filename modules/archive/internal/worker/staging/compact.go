package staging

import (
	"context"
	"fmt"
	"os"
)

// Sizes 是库文件的大小与存活数据量。
type Sizes struct {
	FileBytes int64
	UsedBytes int64
}

// Sizes 按 page_count、freelist_count 与 page_size 计算库文件大小与已用大小（不含 WAL）。
func (s *Store) Sizes(ctx context.Context) (Sizes, error) {
	var pages, free, pageSize int64
	for _, item := range []struct {
		pragma string
		dest   *int64
	}{{"page_count", &pages}, {"freelist_count", &free}, {"page_size", &pageSize}} {
		if err := s.db.QueryRowContext(ctx, "PRAGMA "+item.pragma).Scan(item.dest); err != nil {
			return Sizes{}, fmt.Errorf("PRAGMA %s: %w", item.pragma, err)
		}
	}
	return Sizes{FileBytes: pages * pageSize, UsedBytes: (pages - free) * pageSize}, nil
}

// Compact 在库文件明显大于存活数据时重建：删除数据后空闲页留在库内复用，文件不会自动变小。
// 文件超过存活数据的 ratio 倍且不小于 minBytes 时，VACUUM INTO 新文件 → 关闭连接 → 原子替换 → 重新打开。
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
	fresh := s.path + ".new"
	removeDB(fresh)
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, fresh); err != nil {
		removeDB(fresh)
		return false, fmt.Errorf("copy staging database: %w", err)
	}
	if err := s.db.Close(); err != nil {
		return false, err
	}
	old := s.path + ".old"
	removeDB(old)
	swapErr := os.Rename(s.path, old)
	if swapErr == nil {
		if swapErr = os.Rename(fresh, s.path); swapErr != nil {
			_ = os.Rename(old, s.path) // 换不上就退回旧文件
		}
	}
	// 旧库已在关闭时完成检查点，WAL 与共享内存文件属于旧库，不能留给新库。
	_ = os.Remove(s.path + "-wal")
	_ = os.Remove(s.path + "-shm")
	db, openErr := openDB(s.path, s.opts)
	if openErr != nil {
		return false, fmt.Errorf("reopen staging database after compaction: %w", openErr)
	}
	s.db = db
	if swapErr != nil {
		return false, fmt.Errorf("replace staging file: %w", swapErr)
	}
	removeDB(old)
	return true, nil
}

// removeDB 删除一个库文件及其 WAL 与共享内存文件。
func removeDB(path string) {
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		_ = os.Remove(name)
	}
}
