package alerttext

import (
	"fmt"
	"sort"
	"strings"
)

// Process 是主机上一个进程的占用，来自 HostAgent 上报的进程排行。
type Process struct {
	Name         string
	CPUPercent   float64
	CPUAvailable bool
	RSSBytes     uint64
}

// TopProcesses 把占用最高的几个进程写成一行，例如
// “storage-node CPU 340%、内存 5.1 GB；duckdb CPU 45%、内存 2.3 GB”。
// byMemory 为 true 时按常驻内存排，否则按 CPU 排（只看有 CPU 值的进程）；没有可列的进程时返回空串。
// CPU 是占单核的百分比，与 top 一致：340% 就是占了 3.4 个核。
func TopProcesses(items []Process, byMemory bool, limit int) string {
	ranked := make([]Process, 0, len(items))
	for _, item := range items {
		if byMemory && item.RSSBytes == 0 || !byMemory && !(item.CPUAvailable && item.CPUPercent > 0) {
			continue
		}
		ranked = append(ranked, item)
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if byMemory {
			return ranked[i].RSSBytes > ranked[j].RSSBytes
		}
		return ranked[i].CPUPercent > ranked[j].CPUPercent
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	parts := make([]string, 0, len(ranked))
	for _, item := range ranked {
		var usage []string
		if item.CPUAvailable {
			usage = append(usage, fmt.Sprintf("CPU %.0f%%", item.CPUPercent))
		}
		if item.RSSBytes > 0 {
			usage = append(usage, "内存 "+Bytes(item.RSSBytes))
		}
		parts = append(parts, item.Name+" "+strings.Join(usage, "、"))
	}
	return strings.Join(parts, "；")
}

// Bytes 把字节数写成 GB（一位小数）或 MB（整数）。
func Bytes(value uint64) string {
	const mb, gb = 1 << 20, 1 << 30
	if value >= gb {
		return fmt.Sprintf("%.1f GB", float64(value)/gb)
	}
	return fmt.Sprintf("%.0f MB", float64(value)/mb)
}
