//go:build !windows

package export

import (
	"os"
	"strings"
)

// syncDir 在改名后对目录 fsync，让新文件名落盘。
func syncDir(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer handle.Close()
	// 部分文件系统不支持对目录 fsync，忽略 EINVAL 类错误。
	if err := handle.Sync(); err != nil && !strings.Contains(err.Error(), "invalid argument") {
		return err
	}
	return nil
}
