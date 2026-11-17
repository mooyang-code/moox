package logsink

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

var bootIDMu sync.Mutex

// ProcessBootID 是本进程的启动标识：环境里有 MOOX_BOOT_ID 就用它；没有时生成一个随机值并写回环境变量，
// 这样同一进程里的指标上报、日志上报与健康端口（packages/healthz 按同样的约定读写）用的是同一个值。
// Monitor 按它的变化数启动次数（“反复重启”检查）。
func ProcessBootID() string {
	bootIDMu.Lock()
	defer bootIDMu.Unlock()
	if id := strings.TrimSpace(os.Getenv("MOOX_BOOT_ID")); id != "" {
		return id
	}
	var raw [16]byte
	id := strconv.FormatInt(time.Now().UnixNano(), 36)
	if _, err := rand.Read(raw[:]); err == nil {
		id = hex.EncodeToString(raw[:])
	}
	_ = os.Setenv("MOOX_BOOT_ID", id)
	return id
}
