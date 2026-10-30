//go:build cgo

package duckdb

import (
	"context"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/hostmem"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
)

// 定期记录各 DuckDB 索引的内存与磁盘溢出用量，用来判断单个索引的内存上限是否合适，不必靠推测。
const (
	// 启动后先隔一小会儿采样一次，方便部署后马上确认；之后按固定间隔。
	resourceUsageFirstDelay = 30 * time.Second
	resourceUsageInterval   = 5 * time.Minute
	// 内存用到上限的这个比例就单独提示：此时再大一点的重建或查询就会开始溢出，甚至 OOM。
	nearLimitRatio = 0.8
	// 汇总行里列出内存占用最多的几个索引。
	topUsageIndexes = 3
)

// usageLogf 是用量日志的输出函数，测试里替换它来收集日志。
var usageLogf = log.Printf

type indexUsage struct {
	id string
	// memoryBytes 是该索引当前占用的内存；tempBytes 是已经溢出到临时目录的字节数，大于 0 说明发生过磁盘溢出。
	memoryBytes int64
	tempBytes   int64
}

type usageReport struct {
	indexes []indexUsage
	// limitBytes 是单个索引的内存上限，rssBytes 是整个进程的常驻内存；读不到时为 0。
	limitBytes int64
	limitText  string
	rssBytes   int64
}

// collectUsage 逐个读取已打开索引的 duckdb_memory()。读取失败（索引刚好被关闭等）的索引直接跳过。
func (m *IndexManager) collectUsage() usageReport {
	m.mu.Lock()
	ids := make([]string, 0, len(m.dbs))
	for id := range m.dbs {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	sort.Strings(ids)

	limitText, _ := resolveMemoryLimit()
	report := usageReport{limitText: limitText, limitBytes: hostmem.ParseSize(limitText), rssBytes: processRSSBytes(os.ReadFile)}
	for _, id := range ids {
		m.mu.Lock()
		db := m.dbs[id]
		m.mu.Unlock()
		if db == nil {
			continue
		}
		var used, temp int64
		// 与其它 DuckDB 调用一致，不带可取消的上下文：取消会触发 duckdb_interrupt，并发时可能使进程崩溃。
		ctx, err := duckDBContext(context.Background())
		if err != nil {
			continue
		}
		if err := db.QueryRowContext(ctx, `SELECT COALESCE(SUM(memory_usage_bytes), 0), COALESCE(SUM(temporary_storage_bytes), 0) FROM duckdb_memory()`).Scan(&used, &temp); err != nil {
			continue
		}
		report.indexes = append(report.indexes, indexUsage{id: id, memoryBytes: used, tempBytes: temp})
	}
	return report
}

// describeIndex 把索引 ID（十六进制编码）还原成 空间/View，读不出来时原样返回。
func describeIndex(id string) string {
	ref, err := viewindex.ParseViewIndexID(id)
	if err != nil {
		return id
	}
	return fmt.Sprintf("%s/%s", ref.SpaceID, ref.ViewID)
}

func mib(bytes int64) string { return fmt.Sprintf("%dMB", bytes>>20) }

// lines 生成要写入日志的行：一行汇总，外加每个发生了磁盘溢出或内存接近上限的索引各一行。
func (r usageReport) lines() []string {
	var memory, temp int64
	byMemory := append([]indexUsage(nil), r.indexes...)
	for _, index := range byMemory {
		memory += index.memoryBytes
		temp += index.tempBytes
	}
	sort.SliceStable(byMemory, func(i, j int) bool { return byMemory[i].memoryBytes > byMemory[j].memoryBytes })
	top := make([]string, 0, topUsageIndexes)
	for _, index := range byMemory[:min(topUsageIndexes, len(byMemory))] {
		top = append(top, fmt.Sprintf("%s %s", describeIndex(index.id), mib(index.memoryBytes)))
	}
	summary := fmt.Sprintf("storage view DuckDB 资源 索引=%d 内存合计=%s 溢出临时文件合计=%s 单个上限=%s", len(r.indexes), mib(memory), mib(temp), r.limitText)
	if r.rssBytes > 0 {
		summary += " 进程常驻内存=" + mib(r.rssBytes)
	}
	if len(top) > 0 {
		summary += " 内存最多：" + strings.Join(top, "；")
	}
	lines := []string{summary}
	for _, index := range byMemory {
		switch {
		case index.tempBytes > 0:
			lines = append(lines, fmt.Sprintf("storage view DuckDB 发生磁盘溢出 index=%s 溢出=%s 内存=%s 上限=%s", describeIndex(index.id), mib(index.tempBytes), mib(index.memoryBytes), r.limitText))
		case r.limitBytes > 0 && float64(index.memoryBytes) >= nearLimitRatio*float64(r.limitBytes):
			lines = append(lines, fmt.Sprintf("storage view DuckDB 内存接近上限 index=%s 内存=%s 上限=%s", describeIndex(index.id), mib(index.memoryBytes), r.limitText))
		}
	}
	return lines
}

// logUsageLoop 定期采样并写日志，直到 stop 关闭。
func (m *IndexManager) logUsageLoop(stop <-chan struct{}, firstDelay, interval time.Duration) {
	timer := time.NewTimer(firstDelay)
	defer timer.Stop()
	for {
		select {
		case <-stop:
			return
		case <-timer.C:
		}
		for _, line := range m.collectUsage().lines() {
			usageLogf("%s", line)
		}
		timer.Reset(interval)
	}
}

// processRSSBytes 读取本进程的常驻内存（Linux 的 /proc/self/status），读不到返回 0。
func processRSSBytes(readFile func(string) ([]byte, error)) int64 {
	raw, err := readFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(raw), "\n") {
		// 形如 "VmRSS:	  500000 kB"。
		if fields := strings.Fields(line); len(fields) >= 2 && fields[0] == "VmRSS:" {
			if kb, err := strconv.ParseInt(fields[1], 10, 64); err == nil && kb > 0 {
				return kb << 10
			}
		}
	}
	return 0
}
