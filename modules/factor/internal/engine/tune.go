package engine

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// DetectTotalMemory 探测本机总内存（字节）：Linux 读 /proc/meminfo，macOS 用 sysctl hw.memsize。
func DetectTotalMemory() (int64, error) {
	switch runtime.GOOS {
	case "linux":
		raw, err := os.ReadFile("/proc/meminfo")
		if err != nil {
			return 0, err
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if rest, ok := strings.CutPrefix(line, "MemTotal:"); ok {
				fields := strings.Fields(rest)
				if len(fields) == 0 {
					break
				}
				kb, err := strconv.ParseInt(fields[0], 10, 64)
				if err != nil {
					return 0, err
				}
				return kb << 10, nil
			}
		}
		return 0, errors.New("/proc/meminfo 里没有 MemTotal")
	case "darwin":
		out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
		if err != nil {
			return 0, err
		}
		return strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	}
	return 0, fmt.Errorf("不支持在 %s 上探测内存，请用 --memory 指定", runtime.GOOS)
}
