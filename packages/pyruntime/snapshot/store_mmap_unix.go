//go:build !windows

package snapshot

import (
	"os"

	"golang.org/x/sys/unix"
)

func mapReadOnlyFile(path string, size int64) ([]byte, *os.File, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	data, err := unix.Mmap(int(file.Fd()), 0, int(size), unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	return data, file, nil
}

func closeMappedFile(data []byte, file *os.File) error {
	var firstErr error
	if data != nil {
		if err := unix.Munmap(data); err != nil {
			firstErr = err
		}
	}
	if file != nil {
		if err := file.Close(); firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
