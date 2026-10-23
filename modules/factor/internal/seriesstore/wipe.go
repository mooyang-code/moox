package seriesstore

import (
	"fmt"

	"github.com/cockroachdb/pebble"
)

// wipePrefixes 是需要清掉的键前缀：数据块、各数据源的边界、模式、同步位点与失效重算待办。布局版本键 m/layout 保留。
var wipePrefixes = []string{"k/", "m/b/", "m/schema/", "m/sync/", "m/pending/"}

// Wipe 清空存储里的全部数据与元数据（保留布局版本），并回收空间。进程内执行，不关闭存储：
// 调用方必须先让所有读写者停下（模块已暂停、内存里的数据源已丢弃）。
func (s *Store) Wipe() error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	batch := s.db.NewBatch()
	defer batch.Close()
	for _, prefix := range wipePrefixes {
		if err := batch.DeleteRange([]byte(prefix), prefixEnd([]byte(prefix)), nil); err != nil {
			return fmt.Errorf("清除 %s：%w", prefix, err)
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return err
	}
	s.smu.Lock()
	s.schemas = map[Source][]Field{}
	s.smu.Unlock()
	// 回收磁盘空间；失败只影响空间回收，不影响数据已清空这件事。
	_ = s.db.Compact([]byte("k/"), prefixEnd([]byte("k/")), true)
	return nil
}

// DiskUsage 返回存储占用的磁盘字节数（计划展示用）。
func (s *Store) DiskUsage() int64 { return int64(s.db.Metrics().DiskSpaceUsage()) }
