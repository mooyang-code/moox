//go:build linux

package unitpackage

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func ownedByUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Getuid())
}

func renameExclusive(directory *os.File, old, next string) error {
	return unix.Renameat2(int(directory.Fd()), old, int(directory.Fd()), next, unix.RENAME_NOREPLACE)
}
