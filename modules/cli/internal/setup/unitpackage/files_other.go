//go:build !linux && !darwin

package unitpackage

import (
	"errors"
	"os"
)

func ownedByUser(os.FileInfo) bool { return false }

func renameExclusive(*os.File, string, string) error {
	return errors.New("atomic deployment extraction requires Linux or macOS")
}
