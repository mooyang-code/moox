//go:build cgo

package duckdb

import (
	"errors"
	"io/fs"
	"testing"
)

func TestAutoMemoryLimitScalesWithHostMemory(t *testing.T) {
	const gib = int64(1) << 30
	cases := []struct {
		name  string
		total int64
		want  string
	}{
		{"16GB 主机约等于原来的 256MB", 16 * gib, "256MB"},
		{"线上 Storage 主机 15480MB", 15480 << 20, "241MB"},
		{"32GB 主机", 32 * gib, "512MB"},
		{"4GB 小内存主机取下限", 4 * gib, "128MB"},
		{"1GB 更小的主机仍取下限", gib, "128MB"},
		{"128GB 大内存主机取上限", 128 * gib, "1024MB"},
	}
	for _, tc := range cases {
		if got := autoMemoryLimit(tc.total); got != tc.want {
			t.Errorf("%s：autoMemoryLimit(%d) = %s，want %s", tc.name, tc.total, got, tc.want)
		}
		if !duckDBMemoryLimitRE.MatchString(autoMemoryLimit(tc.total)) {
			t.Errorf("%s：结果 %s 不是 DuckDB 接受的内存值", tc.name, autoMemoryLimit(tc.total))
		}
	}
}

func TestResolveMemoryLimitPrefersTheEnvironment(t *testing.T) {
	restore := hostMemoryBytes
	defer func() { hostMemoryBytes = restore }()
	hostMemoryBytes = func() int64 { return 64 << 30 }

	t.Setenv(duckDBMemoryLimitEnv, "512MB")
	if got, source := resolveMemoryLimit(); got != "512MB" || source == "" {
		t.Fatalf("显式配置应优先：got %q（%s）", got, source)
	}
	t.Setenv(duckDBMemoryLimitEnv, "  ")
	if got, _ := resolveMemoryLimit(); got != "1024MB" {
		t.Fatalf("空白按未配置处理，应按主机内存推算：got %q", got)
	}
}

func TestResolveMemoryLimitFallsBackWhenHostMemoryIsUnknown(t *testing.T) {
	restore := hostMemoryBytes
	defer func() { hostMemoryBytes = restore }()
	hostMemoryBytes = func() int64 { return 0 }
	t.Setenv(duckDBMemoryLimitEnv, "")
	if got, source := resolveMemoryLimit(); got != fallbackMemoryLimit || source == "" {
		t.Fatalf("读不到主机内存时应使用保底值：got %q（%s）", got, source)
	}
}

func TestHostMemoryFromReadsMeminfoAndCgroupLimits(t *testing.T) {
	files := func(content map[string]string) func(string) ([]byte, error) {
		return func(path string) ([]byte, error) {
			if raw, ok := content[path]; ok {
				return []byte(raw), nil
			}
			return nil, fs.ErrNotExist
		}
	}
	meminfo := "MemTotal:       15854312 kB\nMemFree:         1000000 kB\n"
	cases := []struct {
		name  string
		files map[string]string
		want  int64
	}{
		{"只有 meminfo", map[string]string{"/proc/meminfo": meminfo}, 15854312 << 10},
		{"cgroup v2 没有限制（max）时忽略", map[string]string{"/proc/meminfo": meminfo, "/sys/fs/cgroup/memory.max": "max\n"}, 15854312 << 10},
		{"cgroup v2 的限制小于物理内存时采用", map[string]string{"/proc/meminfo": meminfo, "/sys/fs/cgroup/memory.max": "2147483648\n"}, 2 << 30},
		{"cgroup v1 的限制小于物理内存时采用", map[string]string{"/proc/meminfo": meminfo, "/sys/fs/cgroup/memory/memory.limit_in_bytes": "4294967296"}, 4 << 30},
		{"cgroup v1 没有限制时是一个巨大的数，忽略", map[string]string{"/proc/meminfo": meminfo, "/sys/fs/cgroup/memory/memory.limit_in_bytes": "9223372036854771712"}, 15854312 << 10},
		{"meminfo 读不到只有 cgroup 时采用 cgroup", map[string]string{"/sys/fs/cgroup/memory.max": "1073741824"}, 1 << 30},
		{"都读不到", map[string]string{}, 0},
	}
	for _, tc := range cases {
		if got := hostMemoryFrom(files(tc.files)); got != tc.want {
			t.Errorf("%s：got %d，want %d", tc.name, got, tc.want)
		}
	}
	if got := hostMemoryFrom(func(string) ([]byte, error) { return nil, errors.New("denied") }); got != 0 {
		t.Errorf("读取失败应返回 0：got %d", got)
	}
}
