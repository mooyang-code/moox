package pebble

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAutoBlockCacheScalesWithHostMemory(t *testing.T) {
	const gib = int64(1) << 30
	for name, tc := range map[string]struct {
		total int64
		want  int64
	}{
		"线上 Storage 主机 15480MB": {15480 << 20, (15480 << 20) / 32 >> 20 << 20},
		"16GB":                  {16 * gib, 512 << 20},
		"4GB":                   {4 * gib, 128 << 20},
		"1GB 取下限":               {gib, 64 << 20},
		"64GB 刚好到上限":            {64 * gib, 2048 << 20},
		"256GB 仍取上限":            {256 * gib, 2048 << 20},
	} {
		if got := autoBlockCacheBytes(tc.total); got != tc.want {
			t.Errorf("%s：autoBlockCacheBytes(%d) = %d，want %d", name, tc.total, got, tc.want)
		}
	}
}

func TestResolveBlockCachePrefersExplicitThenEnvironmentThenHostMemory(t *testing.T) {
	restore := hostMemoryBytes
	defer func() { hostMemoryBytes = restore }()
	hostMemoryBytes = func() int64 { return 16 << 30 }

	t.Setenv(blockCacheEnv, "300MB")
	if got, source := resolveBlockCache(7 << 20); got != 7<<20 || source == "" {
		t.Fatalf("显式传入应优先：%d（%s）", got, source)
	}
	if got, source := resolveBlockCache(0); got != 300_000_000 || !strings.Contains(source, blockCacheEnv) {
		t.Fatalf("环境变量应优先于主机内存：%d（%s）", got, source)
	}
	t.Setenv(blockCacheEnv, "  ")
	if got, _ := resolveBlockCache(0); got != 512<<20 {
		t.Fatalf("空白按未配置处理，应按主机内存推算：%d", got)
	}
	var logged []string
	restoreLog := cacheLogf
	defer func() { cacheLogf = restoreLog }()
	cacheLogf = func(format string, args ...any) { logged = append(logged, format) }
	t.Setenv(blockCacheEnv, "not-a-size")
	if got, _ := resolveBlockCache(0); got != 512<<20 || len(logged) != 1 {
		t.Fatalf("无效的环境变量应忽略并提示一次：%d %v", got, logged)
	}
	hostMemoryBytes = func() int64 { return 0 }
	t.Setenv(blockCacheEnv, "")
	if got, source := resolveBlockCache(0); got != fallbackBlockCacheMB<<20 || source == "" {
		t.Fatalf("读不到主机内存时应使用保底值：%d（%s）", got, source)
	}
}

func TestOpenAppliesTheBlockCacheAndLogsItsUsage(t *testing.T) {
	var mu sync.Mutex
	var logged []string
	restore := cacheLogf
	defer func() { cacheLogf = restore }()
	cacheLogf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		logged = append(logged, strings.TrimSpace(fmt.Sprintf(format, args...)))
	}

	store, err := Open(Options{Path: t.TempDir(), NodeID: "node-test", BlockCacheBytes: 32 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if store.blockCacheBytes != 32<<20 {
		t.Fatalf("块缓存大小应是传入的 32MB：%d", store.blockCacheBytes)
	}
	var previous cacheStats
	line := store.cacheStatsLine(&previous)
	for _, want := range []string{"Pebble 块缓存", "占用=", "/32MB", "近期命中率=无读取", "累计命中率="} {
		if !strings.Contains(line, want) {
			t.Errorf("用量日志缺少 %q：%s", want, line)
		}
	}
	mu.Lock()
	opened := len(logged) > 0 && strings.Contains(logged[0], "块缓存 32MB")
	mu.Unlock()
	if !opened {
		t.Fatalf("打开时应记录生效的块缓存大小：%v", logged)
	}

	// 定时日志按间隔持续输出，Close 之后协程退出。
	loop, err := Open(Options{Path: t.TempDir(), NodeID: "node-loop", BlockCacheBytes: 16 << 20})
	if err != nil {
		t.Fatal(err)
	}
	loop.startCacheStats(5*time.Millisecond, 5*time.Millisecond)
	deadline := time.After(5 * time.Second)
	for {
		mu.Lock()
		n := 0
		for _, l := range logged {
			if strings.Contains(l, "占用=") {
				n++
			}
		}
		mu.Unlock()
		if n >= 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("定时日志没有持续输出，只有 %d 条", n)
		case <-time.After(5 * time.Millisecond):
		}
	}
	if err := loop.Close(); err != nil {
		t.Fatal(err)
	}
}
