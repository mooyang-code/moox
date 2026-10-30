//go:build cgo

package duckdb

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/hostmem"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
)

func usageTestIndexID(view string) string {
	return viewindex.ViewIndexID("crypto", view)
}

func TestUsageReportSummarizesAndFlagsOnlyTheIndexesThatNeedAttention(t *testing.T) {
	report := usageReport{
		limitText:  "967MB",
		limitBytes: hostmem.ParseSize("967MB"),
		rssBytes:   488 << 20,
		indexes: []indexUsage{
			{id: usageTestIndexID("kline_1h"), memoryBytes: 1 << 20},
			{id: usageTestIndexID("kline_1m"), memoryBytes: 800 << 20},
			{id: usageTestIndexID("factor_1m"), memoryBytes: 120 << 20},
			{id: usageTestIndexID("service_metrics"), memoryBytes: 900 << 20, tempBytes: 35 << 20},
			{id: usageTestIndexID("host_disk"), memoryBytes: 2 << 20},
		},
	}
	lines := report.lines()
	if len(lines) != 3 {
		t.Fatalf("应有 1 行汇总 + 溢出 1 行 + 接近上限 1 行，got %d：%v", len(lines), lines)
	}
	summary := lines[0]
	for _, want := range []string{"索引=5", "内存合计=1823MB", "溢出临时文件合计=35MB", "单个上限=967MB", "进程常驻内存=488MB"} {
		if !strings.Contains(summary, want) {
			t.Errorf("汇总行缺少 %q：%s", want, summary)
		}
	}
	// 内存最多的三个，按用量从大到小，索引 ID 被还原成 空间/View/槽位。
	order := []string{"crypto/service_metrics 900MB", "crypto/kline_1m 800MB", "crypto/factor_1m 120MB"}
	last := -1
	for _, want := range order {
		at := strings.Index(summary, want)
		if at <= last {
			t.Fatalf("汇总行里 %q 缺失或顺序不对：%s", want, summary)
		}
		last = at
	}
	if strings.Contains(summary, "kline_1h") || strings.Contains(summary, "host_disk") {
		t.Errorf("汇总行只列前 %d 个：%s", topUsageIndexes, summary)
	}
	if !strings.Contains(lines[1], "发生磁盘溢出") || !strings.Contains(lines[1], "crypto/service_metrics") || !strings.Contains(lines[1], "溢出=35MB") {
		t.Errorf("溢出行不对：%s", lines[1])
	}
	if !strings.Contains(lines[2], "内存接近上限") || !strings.Contains(lines[2], "crypto/kline_1m") {
		t.Errorf("接近上限行不对：%s", lines[2])
	}
}

func TestUsageReportStaysQuietWhenEverythingIsFarFromTheLimit(t *testing.T) {
	report := usageReport{
		limitText: "967MB", limitBytes: 967_000_000,
		indexes: []indexUsage{{id: usageTestIndexID("kline_1m"), memoryBytes: 100 << 20}, {id: usageTestIndexID("kline_1h"), memoryBytes: 1 << 20}},
	}
	lines := report.lines()
	if len(lines) != 1 || strings.Contains(lines[0], "进程常驻内存") {
		t.Fatalf("没有溢出也不接近上限时只应有汇总行，且读不到 RSS 时不写它：%v", lines)
	}
	if empty := (usageReport{limitText: "256MB"}).lines(); len(empty) != 1 || !strings.Contains(empty[0], "索引=0") {
		t.Fatalf("没有索引时也应能生成汇总行：%v", empty)
	}
}

func TestCollectUsageReadsEveryOpenIndex(t *testing.T) {
	manager, _ := openTempDirManager(t)
	for _, id := range []string{"idx-b", "idx-a"} {
		if err := manager.Prepare(context.Background(), id, tempDirTestSchema()); err != nil {
			t.Fatal(err)
		}
	}
	report := manager.collectUsage()
	if len(report.indexes) != 2 || report.indexes[0].id != "idx-a" || report.indexes[1].id != "idx-b" {
		t.Fatalf("应按 ID 排序读到两个已打开的索引：%+v", report.indexes)
	}
	for _, index := range report.indexes {
		if index.memoryBytes < 0 || index.tempBytes != 0 {
			t.Errorf("索引 %s 用量异常：%+v", index.id, index)
		}
	}
	if report.limitText == "" || report.limitBytes <= 0 {
		t.Errorf("应带上单个索引的内存上限：%+v", report)
	}
	if err := manager.Remove(context.Background(), "idx-a"); err != nil {
		t.Fatal(err)
	}
	if got := manager.collectUsage(); len(got.indexes) != 1 || got.indexes[0].id != "idx-b" {
		t.Fatalf("已删除的索引不应再被统计：%+v", got.indexes)
	}
}

func TestUsageLoopLogsPeriodicallyAndStopsWhenAsked(t *testing.T) {
	manager, _ := openTempDirManager(t)
	if err := manager.Prepare(context.Background(), "idx", tempDirTestSchema()); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var logged []string
	restore := usageLogf
	defer func() { usageLogf = restore }()
	usageLogf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		logged = append(logged, strings.TrimSpace(fmt.Sprintf(format, args...)))
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		manager.logUsageLoop(stop, 5*time.Millisecond, 5*time.Millisecond)
		close(done)
	}()
	deadline := time.After(5 * time.Second)
	for {
		mu.Lock()
		n := len(logged)
		mu.Unlock()
		if n >= 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("定时日志没有按间隔持续输出，只有 %d 条", n)
		case <-time.After(5 * time.Millisecond):
		}
	}
	close(stop)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stop 关闭后日志协程没有退出")
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(logged[0], "storage view DuckDB 资源 索引=1") {
		t.Fatalf("日志内容不对：%s", logged[0])
	}
}

func TestCloseStopsTheUsageLoop(t *testing.T) {
	manager, _ := openTempDirManager(t)
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-manager.stop:
	default:
		t.Fatal("Close 应通知用量日志协程退出")
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("重复 Close 不应报错或 panic：%v", err)
	}
}

func TestProcessRSSBytesReadsVmRSS(t *testing.T) {
	read := func(content string, err error) func(string) ([]byte, error) {
		return func(string) ([]byte, error) { return []byte(content), err }
	}
	if got := processRSSBytes(read("Name:\tmoox\nVmRSS:\t  500000 kB\nThreads:\t9\n", nil)); got != 500000<<10 {
		t.Errorf("got %d", got)
	}
	if got := processRSSBytes(read("", fs.ErrNotExist)); got != 0 {
		t.Errorf("读不到文件应返回 0：%d", got)
	}
	if got := processRSSBytes(read("Name:\tmoox\n", errors.New("x"))); got != 0 {
		t.Errorf("读取出错应返回 0：%d", got)
	}
	if got := processRSSBytes(read("Name:\tmoox\n", nil)); got != 0 {
		t.Errorf("没有 VmRSS 行应返回 0：%d", got)
	}
}

// 真实制造一次磁盘溢出：内存上限 128MB，建一张约 500MB 的临时表，超出的部分写进临时目录。
// 确认 duckdb_memory() 的 temporary_storage_bytes 确实反映溢出量，而不只是“读得到”。
func TestCollectUsageSeesARealDiskSpill(t *testing.T) {
	t.Setenv(duckDBMemoryLimitEnv, "128MB")
	manager, _ := openTempDirManager(t)
	if err := manager.Prepare(context.Background(), "idx", tempDirTestSchema()); err != nil {
		t.Fatal(err)
	}
	// 临时表属于单个连接，要钉住同一个连接建表；建完后用别的连接读取用量，因为 duckdb_memory() 是实例级的。
	conn, err := manager.dbs["idx"].Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), `CREATE TEMP TABLE big AS SELECT range AS i, random() AS r, repeat('x', 20) AS s FROM range(20000000)`); err != nil {
		t.Fatal(err)
	}
	report := manager.collectUsage()
	if len(report.indexes) != 1 || report.indexes[0].tempBytes < 100<<20 {
		t.Fatalf("应读到至少 100MB 的溢出：%+v", report.indexes)
	}
	var spill string
	for _, line := range report.lines() {
		if strings.Contains(line, "发生磁盘溢出") {
			spill = line
		}
	}
	if spill == "" {
		t.Fatalf("发生溢出时应有单独的提示行：%v", report.lines())
	}
}
