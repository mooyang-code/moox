package healthz

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"sync"
)

var (
	binaryOnce   sync.Once
	binarySHA256 string
)

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
