package app

import (
	"os"
	"path/filepath"
)

func mkfile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte("x"), 0o644)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
