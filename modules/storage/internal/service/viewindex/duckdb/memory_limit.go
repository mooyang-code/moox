//go:build cgo

package duckdb

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// 单个 DuckDB 索引的内存上限（memory_limit）。
//
// 每个 View 索引是一个独立的 DuckDB 实例，各自有一份上限。上限是天花板，不是预留：
// 只有几 MB 的索引不会涨到天花板，主机上的实际占用约等于各索引文件大小之和再加重建时的峰值。
// 所以上限应当按“能装下最大的那个索引在重建时的工作集”来定，而不是按“索引个数 × 上限”。
//
// 线上出现过一次：256MB 的上限在重建一个 211MB 的索引（历史回填、激活探测）时被顶满，
// 报 Out of Memory Error: failed to pin block。索引文件超过 max_view_file_bytes（默认 1GiB）才会触发容量重建，
// 一个索引正常可以长到 1GiB，重建它需要的内存约为文件大小的 1 到 1.5 倍，所以上限放到 2GB。
const (
	memoryLimitDivisor = 16
	// DuckDB 在每线程 125MB 以下难以完成检查点和较大的写入批次，所以小内存主机也不低于 128MB。
	minAutoMemoryLimitMB = 128
	maxAutoMemoryLimitMB = 2048
	// 读不到主机内存（非 Linux 开发机、/proc 不可读）时的保底值。
	fallbackMemoryLimit = "256MB"
)

// hostMemoryBytes 返回本进程可用的主机内存字节数，读不到返回 0。测试里替换它来模拟不同规格的主机。
var hostMemoryBytes = detectHostMemoryBytes

// autoMemoryLimit 按主机内存推算单个索引的内存上限，格式是 DuckDB 接受的 "NNMB"。
func autoMemoryLimit(totalBytes int64) string {
	mb := totalBytes / memoryLimitDivisor >> 20
	mb = max(minAutoMemoryLimitMB, min(maxAutoMemoryLimitMB, mb))
	return fmt.Sprintf("%dMB", mb)
}

// resolveMemoryLimit 决定单个索引的内存上限：环境变量显式配置的优先，其次按主机内存推算，读不到主机内存时用保底值。
// 第二个返回值说明取值来源，启动时写进日志，方便确认当前生效的是哪一个。
func resolveMemoryLimit() (limit, source string) {
	if explicit := strings.TrimSpace(os.Getenv(duckDBMemoryLimitEnv)); explicit != "" {
		return explicit, "环境变量 " + duckDBMemoryLimitEnv
	}
	total := hostMemoryBytes()
	if total <= 0 {
		return fallbackMemoryLimit, "读不到主机内存，使用保底值"
	}
	return autoMemoryLimit(total), fmt.Sprintf("主机内存 %dMB 的 1/%d，下限 %dMB、上限 %dMB", total>>20, memoryLimitDivisor, minAutoMemoryLimitMB, maxAutoMemoryLimitMB)
}

// detectHostMemoryBytes 读取 Linux 主机的物理内存；在容器里取 cgroup 限制和物理内存中较小的一个。
func detectHostMemoryBytes() int64 {
	return hostMemoryFrom(os.ReadFile)
}

func hostMemoryFrom(readFile func(string) ([]byte, error)) int64 {
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
