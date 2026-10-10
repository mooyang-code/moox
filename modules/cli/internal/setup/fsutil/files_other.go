//go:build !linux && !darwin

package fsutil

import (
	"errors"
	"os"
)

func Owned(os.FileInfo) bool { return false }

func RenameExclusive(*os.File, string, string) error {
	return errors.New("atomic deployment extraction requires Linux or macOS")
}
