//go:build !windows

package store

import "os"

func privateFile(info os.FileInfo) bool      { return info.Mode().Perm() == 0o600 }
func privateDirectory(info os.FileInfo) bool { return info.Mode().Perm() == 0o700 }
func syncDirectory(root *os.Root) error {
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}
