//go:build !windows

package security

import (
	"fmt"
	"os"
)

func checkPrivateAccess(path string, info os.FileInfo) error {
	if info.Mode().Perm() != 0o600 {
		return fmt.Errorf("%s must have mode 0600, got %04o", path, info.Mode().Perm())
	}
	return nil
}
