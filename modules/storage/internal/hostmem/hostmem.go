// Package hostmem 读取本机（或容器）可用的物理内存，并解析 "256MB" 这类内存写法。
// Storage 里按主机内存推算缓存和上限的地方（DuckDB 的单索引内存上限、Pebble 的块缓存）共用它。
package hostmem

import (
	"os"
	"strconv"
	"strings"
)

// TotalBytes 返回本进程可用的主机内存字节数，读不到返回 0。
// Linux 读 /proc/meminfo；在容器里再取 cgroup 限制和物理内存中较小的一个。
func TotalBytes() int64 { return From(os.ReadFile) }

// From 是 TotalBytes 的实现，读文件的函数可替换，便于测试。
func From(readFile func(string) ([]byte, error)) int64 {
	var total int64
	if raw, err := readFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			// 形如 "MemTotal:       15854312 kB"。
			if fields := strings.Fields(line); len(fields) >= 2 && fields[0] == "MemTotal:" {
				if kb, err := strconv.ParseInt(fields[1], 10, 64); err == nil && kb > 0 {
					total = kb << 10
				}
				break
			}
		}
	}
	// cgroup v2 与 v1 的内存上限；没有限制时前者是 "max"，后者是一个接近 2^63 的大数，都不会小于物理内存而被忽略。
	for _, path := range []string{"/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory/memory.limit_in_bytes"} {
		raw, err := readFile(path)
		if err != nil {
			continue
		}
		if limit, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64); err == nil && limit > 0 && (total == 0 || limit < total) {
			total = limit
		}
	}
	return total
}

// ParseSize 把 "967MB" 这样的内存写法转成字节，KB/MB/GB/TB 按 1000 进制（与 DuckDB 一致）。读不出来返回 0。
func ParseSize(text string) int64 {
	text = strings.TrimSpace(text)
	for suffix, unit := range map[string]int64{"KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12} {
		if strings.HasSuffix(text, suffix) {
			if n, err := strconv.ParseInt(strings.TrimSuffix(text, suffix), 10, 64); err == nil && n > 0 {
				return n * unit
			}
		}
	}
	return 0
}
