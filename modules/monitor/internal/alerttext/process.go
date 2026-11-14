package alerttext

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Process 是主机上一个进程的占用，来自 HostAgent 上报的进程排行。
type Process struct {
	// PID 用来区分同名的进程（例如几个 python）；只记在告警事件里，正文不写。
	PID          uint32
	Name         string
	CPUPercent   float64
	CPUAvailable bool
	RSSBytes     uint64
}

// processJSON 是进程在告警事件内容里的写法：没有 CPU 值时不写 cpu_percent。
type processJSON struct {
	PID        uint32   `json:"pid"`
	Name       string   `json:"name"`
	RSSBytes   uint64   `json:"rss_bytes"`
	CPUPercent *float64 `json:"cpu_percent,omitempty"`
}

func (p Process) MarshalJSON() ([]byte, error) {
	item := processJSON{PID: p.PID, Name: p.Name, RSSBytes: p.RSSBytes}
	if p.CPUAvailable {
		item.CPUPercent = &p.CPUPercent
	}
	return json.Marshal(item)
}

func (p *Process) UnmarshalJSON(raw []byte) error {
	var item processJSON
	if err := json.Unmarshal(raw, &item); err != nil {
		return err
	}
	*p = Process{PID: item.PID, Name: item.Name, RSSBytes: item.RSSBytes}
	if item.CPUPercent != nil {
		p.CPUPercent, p.CPUAvailable = *item.CPUPercent, true
	}
	return nil
}

// ProcessDetail 是检查结果附带的进程排行：主机 CPU、内存检查超限时写进结果的诊断信息，
// 告警评估把它原样记进事件内容，并在推送正文里列出前几个；页面从事件内容里读。
type ProcessDetail struct {
	// Metric 是产生排行的指标（cpu、memory），决定按 CPU 还是按内存排。
	Metric       string    `json:"metric,omitempty"`
	TopProcesses []Process `json:"top_processes,omitempty"`
}

// ByMemory 报告排行是否按常驻内存排（内存检查）；其余按 CPU 排。
func (d ProcessDetail) ByMemory() bool { return d.Metric == "memory" }

// Encode 把排行写成检查结果的诊断信息；没有进程时返回空串。
func (d ProcessDetail) Encode() string {
	if len(d.TopProcesses) == 0 {
		return ""
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return ""
	}
	return string(raw)
}

// DecodeProcessDetail 从检查结果的诊断信息或告警事件内容里读出进程排行；没有时返回零值。
func DecodeProcessDetail(raw string) ProcessDetail {
	var detail ProcessDetail
	if json.Unmarshal([]byte(raw), &detail) != nil || len(detail.TopProcesses) == 0 {
		return ProcessDetail{}
	}
	return detail
}

// TopProcesses 把占用最高的几个进程写成一行，例如
// “storage-primary CPU 340%、内存 5.1 GB；duckdb CPU 45%、内存 2.3 GB”。
// byMemory 为 true 时按常驻内存排，否则按 CPU 排（只看有 CPU 值的进程）；没有可列的进程时返回空串。
// CPU 是占单核的百分比，与 top 一致：340% 就是占了 3.4 个核。
func TopProcesses(items []Process, byMemory bool, limit int) string {
	ranked := RankProcesses(items, byMemory, limit)
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

// RankProcesses 按告警的指标挑出占用最高的几个进程：byMemory 为 true 时按常驻内存从高到低（去掉内存为 0 的），
// 否则按 CPU 从高到低（只留取到了 CPU 且大于 0 的）。HostAgent 上报的是“CPU 前几名”和“内存前几名”合在一起、按 CPU 排的，
// 直接取前几个的话，内存告警里会看不到真正占内存的进程。
func RankProcesses(items []Process, byMemory bool, limit int) []Process {
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
	return ranked
}

// Bytes 把字节数写成 GB（一位小数）或 MB（整数）。
func Bytes(value uint64) string {
	const mb, gb = 1 << 20, 1 << 30
	if value >= gb {
		return fmt.Sprintf("%.1f GB", float64(value)/gb)
	}
	return fmt.Sprintf("%.0f MB", float64(value)/mb)
}
