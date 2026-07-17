//go:build windows

package snapshot

import (
	"io"
	"os"
)

func mapReadOnlyFile(path string, size int64) ([]byte, *os.File, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, size+1))
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if int64(len(data)) != size {
		_ = file.Close()
		return nil, nil, io.ErrUnexpectedEOF
	}
	return data, file, nil
}

func closeMappedFile(_ []byte, file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Close()
}
