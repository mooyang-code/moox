//go:build darwin

package fsutil

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func Owned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Getuid())
}

func SingleLink(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink == 1
}

func RenameExclusive(directory *os.File, old, next string) error {
	return unix.RenameatxNp(int(directory.Fd()), old, int(directory.Fd()), next, unix.RENAME_EXCL)
}
