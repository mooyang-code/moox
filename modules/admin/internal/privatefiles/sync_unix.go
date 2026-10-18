//go:build darwin || linux

package privatefiles

import "os"

// SyncDirectory persists a newly published filename as well as its contents.
func SyncDirectory(root *os.Root) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
