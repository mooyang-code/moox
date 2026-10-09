package privatefiles

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Lock coordinates independent deployment processes. Closing the returned
// file releases the OS lock, including when its process exits unexpectedly.
func Lock(root *os.Root, name string) (*os.File, error) {
	if filepath.Base(name) != name || name == "." || name == ".." {
		return nil, errors.New("credential lock must be a single filename")
	}
	if info, err := root.Lstat(name); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return nil, errors.New("credential lock must be a regular 0600 file")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	// Separate creation and existing-file opens to avoid concurrent O_CREATE
	// opens returning ENOENT on the macOS filesystem.
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if os.IsExist(err) {
		file, err = root.OpenFile(name, os.O_RDWR, 0o600)
	}
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	current, statErr := root.Lstat(name)
	if err != nil || statErr != nil || !opened.Mode().IsRegular() || opened.Mode().Perm() != 0o600 || !os.SameFile(opened, current) {
		file.Close()
		return nil, errors.New("credential lock changed while opening")
	}
	if err := lockFile(file); err != nil {
		file.Close()
		return nil, fmt.Errorf("lock private credential: %w", err)
	}
	return file, nil
}
