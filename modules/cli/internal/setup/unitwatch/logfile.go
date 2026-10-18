package unitwatch

import (
	"fmt"
	"os"
)

// 守护日志 run/watchdog.log 的轮转：每分钟一行汇总，超过 10 MiB 时依次改名为 .1 ～ .3。
const (
	logMaxBytes = 10 << 20
	logBackups  = 3
)

// OpenLog 打开（必要时先轮转）守护日志，以追加方式写入，权限 0600。
func OpenLog(path string) (*os.File, error) {
	if info, err := os.Stat(path); err == nil && info.Size() >= logMaxBytes {
		for i := logBackups - 1; i >= 1; i-- {
			_ = os.Rename(fmt.Sprintf("%s.%d", path, i), fmt.Sprintf("%s.%d", path, i+1))
		}
		if err := os.Rename(path, path+".1"); err != nil {
			return nil, err
		}
	}
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
}
