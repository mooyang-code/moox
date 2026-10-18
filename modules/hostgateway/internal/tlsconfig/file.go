package tlsconfig

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func readFile(path string, private bool, limit int64) ([]byte, error) {
	if path == "" || path != strings.TrimSpace(path) || strings.ContainsAny(path, "\x00\r\n") {
		return nil, errors.New("a regular file path is required")
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, errors.New("cannot open certificate directory")
	}
	defer root.Close()
	name := filepath.Base(path)
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("must be a regular file, not a symlink")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, errors.New("cannot open certificate file")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, errors.New("certificate file changed while opening")
	}
	// Windows's Mode bits cannot express the owner's ACL. Deployment must set
	// private ACLs there; Unix deployments use the exact private-file contract.
	if private && runtime.GOOS != "windows" && opened.Mode().Perm() != 0o600 {
		return nil, errors.New("private key file permissions must be 0600")
	}
	encoded, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || len(encoded) > int(limit) || len(encoded) == 0 {
		return nil, errors.New("certificate file is empty, unreadable or exceeds its size limit")
	}
	return encoded, nil
}
