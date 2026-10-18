package pebble

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/hostmem"
)

// Pebble 块缓存。
//
// Pebble 默认只有 8MiB 块缓存，而 DataNode 的数据是 GB 级（线上约 1.6GB）。读窗口时每一行、每一列都是一次点查，
// 缓存装不下时，每次点查都要把包含它的数据块和索引块重新读出并 snappy 解压：
// 2026-10-14 对 1h K 线因子集做回填，DataNode 的 CPU 被打到 330% 以上，profile 里 52% 的时间在 snappy.decode，
// 其中 72% 是在解压索引块，读单个窗口要 15 秒以上并超时重试。
// 缓存放大到能装下热点的索引块和数据块后，这部分解压就不再重复。
const (
	blockCacheEnv = "MOOX_STORAGE_NODE_BLOCK_CACHE"
	// 取主机内存的 1/32：16GB 的主机约 500MB，比 DataNode 进程本身的占用（约 100MB）大，但远小于主机内存。
	blockCacheDivisor = 32
	minBlockCacheMB   = 64
	maxBlockCacheMB   = 2048
	// 读不到主机内存时的保底值。
	fallbackBlockCacheMB = 128

	cacheStatsFirstDelay = 30 * time.Second
	cacheStatsInterval   = 5 * time.Minute
)

// hostMemoryBytes 返回主机内存，测试里替换它来模拟不同规格的主机。
var hostMemoryBytes = hostmem.TotalBytes

// cacheLogf 是缓存用量日志的输出函数，测试里替换它来收集日志。
var cacheLogf = log.Printf

// autoBlockCacheBytes 按主机内存推算块缓存大小。
func autoBlockCacheBytes(totalBytes int64) int64 {
	mb := totalBytes / blockCacheDivisor >> 20
	return max(minBlockCacheMB, min(maxBlockCacheMB, mb)) << 20
}

// resolveBlockCache 决定块缓存大小：显式传入的优先（测试用），其次环境变量，再按主机内存推算，读不到主机内存时用保底值。
// 第二个返回值说明来源，打开存储时写进日志。
func resolveBlockCache(explicit int64) (bytes int64, source string) {
	if explicit > 0 {
		return explicit, "代码传入"
	}
	if text := strings.TrimSpace(os.Getenv(blockCacheEnv)); text != "" {
		if bytes := hostmem.ParseSize(text); bytes > 0 {
			return bytes, "环境变量 " + blockCacheEnv
		}
		cacheLogf("storage node 环境变量 %s=%q 不是有效的内存值（正整数加 KB/MB/GB/TB），改用按主机内存推算的值", blockCacheEnv, text)
	}
	total := hostMemoryBytes()
	if total <= 0 {
		return fallbackBlockCacheMB << 20, "读不到主机内存，使用保底值"
	}
	return autoBlockCacheBytes(total), fmt.Sprintf("主机内存 %dMB 的 1/%d，下限 %dMB、上限 %dMB", total>>20, blockCacheDivisor, minBlockCacheMB, maxBlockCacheMB)
}

// cacheStats 记录上一次采样时的命中与未命中次数，用来算这一段时间的命中率。
type cacheStats struct {
	hits, misses int64
}

// line 读取当前块缓存用量，返回一行日志；命中率是自上次调用以来的，累计命中率附在后面。
func (s *Store) cacheStatsLine(previous *cacheStats) string {
	metrics := s.db.Metrics().BlockCache
	deltaHits, deltaMisses := metrics.Hits-previous.hits, metrics.Misses-previous.misses
	previous.hits, previous.misses = metrics.Hits, metrics.Misses
	rate := func(hits, misses int64) string {
		if hits+misses == 0 {
			return "无读取"
		}
		return fmt.Sprintf("%.1f%%", 100*float64(hits)/float64(hits+misses))
	}
	return fmt.Sprintf("storage node Pebble 块缓存 占用=%dMB/%dMB 条目=%d 近期命中率=%s（命中 %d、未命中 %d）累计命中率=%s",
		metrics.Size>>20, s.blockCacheBytes>>20, metrics.Count, rate(deltaHits, deltaMisses), deltaHits, deltaMisses, rate(metrics.Hits, metrics.Misses))
}

// startCacheStats 定期记录块缓存的命中率与占用，用来判断缓存大小是否合适，直到 Store 关闭。
func (s *Store) startCacheStats(firstDelay, interval time.Duration) {
	s.maintenanceWG.Add(1)
	go func() {
		defer s.maintenanceWG.Done()
		timer := time.NewTimer(firstDelay)
		defer timer.Stop()
		var previous cacheStats
		for {
			select {
			case <-s.statsStop:
				return
			case <-timer.C:
			}
			cacheLogf("%s", s.cacheStatsLine(&previous))
			timer.Reset(interval)
		}
	}()
}
