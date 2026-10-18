//go:build unix

package hostpolicy

import (
	"errors"
	"os"
	"syscall"
)

// checkPrivate 要求文件只有属主可读写（0600）且属于当前用户。
func checkPrivate(info os.FileInfo) error {
	if info.Mode().Perm() != 0o600 {
		return errors.New("host policy must have permission 0600")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Uid != uint32(os.Getuid()) {
		return errors.New("host policy must be owned by the current user")
	}
	return nil
}
