//go:build darwin || linux

package device

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// FreeDiskBytes reports bytes available to an unprivileged process on the
// filesystem containing path. The nearest existing parent is used.
func FreeDiskBytes(path string) (uint64, error) {
	current := filepath.Clean(path)
	for {
		if _, err := os.Stat(current); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return 0, err
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	var stats unix.Statfs_t
	if err := unix.Statfs(current, &stats); err != nil {
		return 0, err
	}
	return stats.Bavail * uint64(stats.Bsize), nil
}
