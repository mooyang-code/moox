package collector

import (
	"bytes"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
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
	// CPUTicks 是进程自己用户态加内核态的累计 CPU 时间（时钟滴答数），含全部线程，不含子进程。
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
			return ProcStat{}, fmt.Errorf("proc stat: invalid field %d", item.index+3)
		}
		*item.value = parsed
	}
	stat.CPUTicks = user + system
	return stat, nil
}

// ProcessName 给出进程在告警和页面上的名字：优先用可执行文件的文件名（exe 是 /proc/<pid>/exe 指向的路径），
// 读不到时（别的用户的进程、内核线程）用 comm。
//
// 不读 /proc/<pid>/cmdline，原因有两个：进程可以改写自己的命令行（sshd、postgres、nginx、Node 的 process.title 都会），
// 改写后的内容可能带参数甚至密钥；而且内核读 cmdline 要拿目标进程的内存映射锁，目标进程卡住时会把采集一起卡住。
//
// 两种来源的处理不同：
//   - exe 是一个真实的路径：去掉发布换版后内核加的 " (deleted)" 后缀，取文件名。路径里的空格是文件名的一部分，不切。
//   - comm 可以被进程改写成任意内容（例如 "sshd: user@pts/0"），只取空白之前的第一段并去掉结尾的冒号。
//     它不是路径，不取文件名：内核线程的名字本身带斜杠（ksoftirqd/0、kworker/1:2-events）。
//
// 名字会进告警正文，最后统一把控制字符换成问号。
func ProcessName(exe, comm string) string {
	name := ""
	if exe = strings.TrimSuffix(exe, " (deleted)"); exe != "" {
		if base := filepath.Base(exe); base != "." && base != "/" {
			name = base
		}
	}
	if name == "" {
		if fields := strings.Fields(comm); len(fields) > 0 {
			name = strings.TrimSuffix(fields[0], ":")
		}
	}
	name = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == utf8.RuneError {
			return '?'
		}
		return r
	}, strings.ToValidUTF8(name, "?")))
	if name == "" {
		return "?"
	}
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
// 所以“进程增量 ÷ 整机增量 × 核数”就是占单核的百分比，不需要知道时钟频率。cpuTotalDelta 为 0（第一次采样、计数回绕）时只报内存。
// cores 是 /proc/stat 里的核数（全部在线的核，与整机增量同一口径）。name 只对入选的进程调用。
func (t *processTracker) top(stats []ProcStat, cpuTotalDelta uint64, cores int, pageSize uint64, name func(ProcStat) string) []*hostmetricpb.ProcessMetric {
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
		if previous, ok := t.previous[stat.PID]; ok && cpuTotalDelta > 0 &&
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

	chosen := make([]candidate, 0, len(selected))
	for _, item := range selected {
		chosen = append(chosen, item)
	}
	sort.Slice(chosen, func(i, j int) bool {
		if chosen[i].cpu != chosen[j].cpu {
			return chosen[i].cpu > chosen[j].cpu
		}
		if chosen[i].rss != chosen[j].rss {
			return chosen[i].rss > chosen[j].rss
		}
		return chosen[i].stat.PID < chosen[j].stat.PID
	})
	// 先排好序再取名字：取名字要读 /proc，按固定的顺序读，结果和读取顺序都是确定的。
	out := make([]*hostmetricpb.ProcessMetric, 0, len(chosen))
	for _, item := range chosen {
		out = append(out, &hostmetricpb.ProcessMetric{
			Pid: uint32(item.stat.PID), Name: name(item.stat),
			CpuPercent: item.cpu, CpuAvailable: item.cpuAvailable, RssBytes: item.rss,
		})
	}
	return out
}

// reset 丢掉上一次采样的基准。某一轮没能读到进程数据时调用：否则下一轮会拿两轮之前的进程 CPU 时间去除一轮的整机增量，
// 算出的占用会偏大。
func (t *processTracker) reset() { t.previous = nil }

// CountCPUs 数 /proc/stat 里 cpu0、cpu1… 的行数，即全部在线的核。数不出来时返回 0，由调用方退回别的办法。
func CountCPUs(procStat []byte) int {
	count := 0
	for _, line := range bytes.Split(procStat, []byte{'\n'}) {
		if len(line) > 3 && bytes.HasPrefix(line, []byte("cpu")) && line[3] >= '0' && line[3] <= '9' {
			count++
		}
	}
	return count
}
