//go:build cgo

package duckdb

import (
	"fmt"
	"os"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/hostmem"
)

// 单个 DuckDB 索引的内存上限（memory_limit）。
//
// 每个 View 索引是一个独立的 DuckDB 实例，各自有一份上限。上限是天花板，不是预留：
// 只有几 MB 的索引不会涨到天花板，主机上的实际占用约等于各索引文件大小之和再加建索引时的峰值。
// 所以上限应当按“能装下最大的那个索引在建索引时的工作集”来定，而不是按“索引个数 × 上限”。
//
// 线上出现过一次：256MB 的上限在建一个 211MB 的索引（历史回填）时被顶满，
// 报 Out of Memory Error: failed to pin block。上限够用时 DuckDB 会淘汰缓存，内存不随文件线性增长：
// 用线上 523 个序列、34 列的 1m 因子索引实测，按建索引的真实写法回填到每序列 1 万根（文件 1.67GB），
// 967MB 上限下峰值 909MB、没有溢出。
// 索引大小由定时删除最旧数据封顶：每个序列只保留最新的 bars 根（默认 1 万根）；
// max_view_file_bytes（默认 4GiB）只是告警线。上限封顶 2GB，16GB 主机上按 1/16 取到约 1GB。
const (
	memoryLimitDivisor = 16
	// DuckDB 在每线程 125MB 以下难以完成检查点和较大的写入批次，所以小内存主机也不低于 128MB。
	minAutoMemoryLimitMB = 128
	maxAutoMemoryLimitMB = 2048
	// 读不到主机内存（非 Linux 开发机、/proc 不可读）时的保底值。
	fallbackMemoryLimit = "256MB"
)

// hostMemoryBytes 返回本进程可用的主机内存字节数，读不到返回 0。测试里替换它来模拟不同规格的主机。
var hostMemoryBytes = hostmem.TotalBytes

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
