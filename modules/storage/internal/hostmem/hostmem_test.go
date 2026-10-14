package hostmem

import (
	"errors"
	"io/fs"
	"testing"
)

func TestFromReadsMeminfoAndCgroupLimits(t *testing.T) {
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
		if got := From(files(tc.files)); got != tc.want {
			t.Errorf("%s：got %d，want %d", tc.name, got, tc.want)
		}
	}
	if got := From(func(string) ([]byte, error) { return nil, errors.New("denied") }); got != 0 {
		t.Errorf("读取失败应返回 0：got %d", got)
	}
}

func TestParseSizeUsesDecimalUnits(t *testing.T) {
	for text, want := range map[string]int64{"256MB": 256_000_000, "2GB": 2_000_000_000, "512KB": 512_000, "1TB": 1_000_000_000_000, " 64MB ": 64_000_000, "": 0, "abc": 0, "0MB": 0, "12MiB": 0} {
		if got := ParseSize(text); got != want {
			t.Errorf("ParseSize(%q) = %d，want %d", text, got, want)
		}
	}
}
