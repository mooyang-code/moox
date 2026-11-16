package healthz

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	bootIDMu     sync.Mutex
	binaryOnce   sync.Once
	binarySHA256 string
)

// processBootID 是本进程的启动标识：环境里有 MOOX_BOOT_ID 就用它；没有时生成一个随机值并写回环境变量，
// 和 packages/report/logsink.ProcessBootID 是同一个约定，同一进程里的健康端口与各路上报用的是同一个值。
func processBootID() string {
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

// processBinarySHA256 是本进程可执行文件的摘要（小写十六进制），启动后第一次用到时算一次；读不到时为空。
func processBinarySHA256() string {
	binaryOnce.Do(func() {
		name, err := os.Executable()
		if err != nil {
			return
		}
		file, err := os.Open(name)
		if err != nil {
			return
		}
		defer file.Close()
		hash := sha256.New()
		if _, err := io.Copy(hash, file); err != nil {
			return
		}
		binarySHA256 = hex.EncodeToString(hash.Sum(nil))
	})
	return binarySHA256
}
