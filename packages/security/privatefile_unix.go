//go:build !windows

package security

import (
	"fmt"
	"os"
	"syscall"
)

func checkPrivateAccess(path string, info os.FileInfo) error {
	if info.Mode().Perm() != 0o600 {
		return fmt.Errorf("%s must have mode 0600, got %04o", path, info.Mode().Perm())
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("%s must be owned by the current user", path)
	}
	return nil
}
