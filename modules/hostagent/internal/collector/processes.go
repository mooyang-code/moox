package collector

import (
	"bytes"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/mooyang-code/moox/packages/hostmetricpb"
)

const (
	// topProcessCount 是 CPU、内存各取的进程数；合并去重后最多上报两倍。
	topProcessCount    = 5
	maxProcessNameRune = 64
)

// ProcStat 是 /proc/<pid>/stat 里用得到的几项。
type ProcStat struct {
	PID  int
	Comm string
	// StartTime 是进程启动时刻（开机以来的时钟滴答数），与 PID 一起区分“同一个 PID 换了进程”。
	StartTime uint64
	// CPUTicks 是用户态加内核态的累计 CPU 时间（时钟滴答数）。
	CPUTicks uint64
	RSSPages uint64
}

// ParseProcStat 解析 /proc/<pid>/stat。进程名在括号里，可能含空格和括号，所以按第一个 "(" 与最后一个 ")" 切。
func ParseProcStat(data []byte) (ProcStat, error) {
	open, closing := bytes.IndexByte(data, '('), bytes.LastIndexByte(data, ')')
	if open < 0 || closing < open {
		return ProcStat{}, fmt.Errorf("proc stat: comm not found")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data[:open])))
	if err != nil || pid <= 0 {
		return ProcStat{}, fmt.Errorf("proc stat: invalid pid")
	}
	// 括号之后从第 3 个字段（state）开始：utime 是第 14 个、stime 第 15 个、starttime 第 22 个、rss 第 24 个。
	fields := strings.Fields(string(data[closing+1:]))
	const utime, stime, starttime, rss = 14 - 3, 15 - 3, 22 - 3, 24 - 3
	if len(fields) <= rss {
		return ProcStat{}, fmt.Errorf("proc stat: too few fields")
	}
	stat := ProcStat{PID: pid, Comm: string(data[open+1 : closing])}
	var user, system uint64
	for _, item := range []struct {
		index int
		value *uint64
	}{{utime, &user}, {stime, &system}, {starttime, &stat.StartTime}, {rss, &stat.RSSPages}} {
		parsed, err := strconv.ParseUint(fields[item.index], 10, 64)
		if err != nil {
			// rss 在个别内核上可能是负数（内核线程记账），按 0 处理；其余字段出错就是格式不对。
			if item.index == rss {
				continue
			}
			return ProcStat{}, fmt.Errorf("proc stat: invalid field %d", item.index+3)
		}
		*item.value = parsed
	}
	stat.CPUTicks = user + system
	return stat, nil
}

// ProcessName 取命令行第一个参数的文件名；/proc/<pid>/comm 最多 15 个字符，长的进程名会被截断。
// 内核线程没有命令行，用 comm。只取第一个参数：后面的参数里可能有密钥。
func ProcessName(cmdline []byte, comm string) string {
	first, _, _ := bytes.Cut(cmdline, []byte{0})
	name := strings.TrimSpace(string(first))
	if name != "" {
		name = filepath.Base(name)
	}
	if name == "" || name == "." || name == "/" {
		name = comm
	}
	name = strings.ToValidUTF8(name, "?")
	if utf8.RuneCountInString(name) > maxProcessNameRune {
		name = string([]rune(name)[:maxProcessNameRune])
	}
	return name
}

type processPrevious struct {
	startTime uint64
	cpuTicks  uint64
}

// processTracker 记住上一次采样里每个进程的累计 CPU 时间，用两次采样的差算 CPU 占用。
type processTracker struct {
	previous map[int]processPrevious
}

// top 返回 CPU 前几名与内存前几名（合并去重），并把本次采样记为下一次的基准。
//
// cpuTotalDelta 是两次采样之间整机 CPU 时间的增量（/proc/stat 各项之和，时钟滴答数），与进程的 CPU 时间同一个单位，
// 所以“进程增量 ÷ 整机增量 × 核数”就是占单核的百分比，不需要知道时钟频率。cpuDeltaOK 为 false（第一次采样、计数回绕）时只报内存。
// name 只对入选的进程调用。
func (t *processTracker) top(stats []ProcStat, cpuTotalDelta uint64, cpuDeltaOK bool, cores int, pageSize uint64, name func(ProcStat) string) []*hostmetricpb.ProcessMetric {
	type candidate struct {
		stat         ProcStat
		cpu          float64
		cpuAvailable bool
		rss          uint64
	}
	candidates := make([]candidate, 0, len(stats))
	next := make(map[int]processPrevious, len(stats))
	for _, stat := range stats {
		next[stat.PID] = processPrevious{startTime: stat.StartTime, cpuTicks: stat.CPUTicks}
		item := candidate{stat: stat, rss: stat.RSSPages * pageSize}
		// 启动时刻不同说明 PID 被新进程复用了，不能和上一次的值相减。
		if previous, ok := t.previous[stat.PID]; ok && cpuDeltaOK && cpuTotalDelta > 0 &&
			previous.startTime == stat.StartTime && stat.CPUTicks >= previous.cpuTicks {
			item.cpu = float64(stat.CPUTicks-previous.cpuTicks) * 100 * float64(cores) / float64(cpuTotalDelta)
			item.cpuAvailable = true
		}
		candidates = append(candidates, item)
	}
	t.previous = next

	selected := map[int]candidate{}
	pick := func(less func(a, b candidate) bool, keep func(candidate) bool) {
		sort.SliceStable(candidates, func(i, j int) bool { return less(candidates[i], candidates[j]) })
		taken := 0
		for _, item := range candidates {
			if taken == topProcessCount || !keep(item) {
				break
			}
			selected[item.stat.PID] = item
			taken++
		}
	}
	pick(func(a, b candidate) bool {
		if a.cpu != b.cpu {
			return a.cpu > b.cpu
		}
		return a.stat.PID < b.stat.PID
	}, func(item candidate) bool { return item.cpuAvailable && item.cpu > 0 })
	pick(func(a, b candidate) bool {
		if a.rss != b.rss {
			return a.rss > b.rss
		}
		return a.stat.PID < b.stat.PID
	}, func(item candidate) bool { return item.rss > 0 })

	out := make([]*hostmetricpb.ProcessMetric, 0, len(selected))
	for _, item := range selected {
		out = append(out, &hostmetricpb.ProcessMetric{
			Pid: uint32(item.stat.PID), Name: name(item.stat),
			CpuPercent: item.cpu, CpuAvailable: item.cpuAvailable, RssBytes: item.rss,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CpuPercent != out[j].CpuPercent {
			return out[i].CpuPercent > out[j].CpuPercent
		}
		if out[i].RssBytes != out[j].RssBytes {
			return out[i].RssBytes > out[j].RssBytes
		}
		return out[i].Pid < out[j].Pid
	})
	return out
}
