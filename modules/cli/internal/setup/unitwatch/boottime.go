package unitwatch

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
)

// systemBootTime 读 /proc/stat 的 btime，用于“主机重启”告警里的开机时间；测试可替换。读不到时告警里不写开机时间。
var systemBootTime = func() (time.Time, error) {
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if fields := strings.Fields(line); len(fields) == 2 && fields[0] == "btime" {
			seconds, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				return time.Time{}, err
			}
			return time.Unix(seconds, 0), nil
		}
	}
	return time.Time{}, errors.New("btime missing from /proc/stat")
}
