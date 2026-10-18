//go:build cgo

package duckdb

import "testing"

func TestAutoMemoryLimitScalesWithHostMemory(t *testing.T) {
	const gib = int64(1) << 30
	cases := []struct {
		name  string
		total int64
		want  string
	}{
		{"线上 Storage 主机 15480MB", 15480 << 20, "967MB"},
		{"16GB 主机", 16 * gib, "1024MB"},
		{"8GB 主机", 8 * gib, "512MB"},
		{"4GB 主机", 4 * gib, "256MB"},
		{"1GB 更小的主机取下限", gib, "128MB"},
		{"32GB 主机刚好到上限", 32 * gib, "2048MB"},
		{"128GB 大内存主机仍取上限", 128 * gib, "2048MB"},
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
	if got, _ := resolveMemoryLimit(); got != "2048MB" {
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
