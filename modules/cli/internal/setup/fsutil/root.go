package fsutil

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func OpenPhysicalRoot(name string, private bool) (*os.Root, error) {
	if name == "" || !filepath.IsAbs(name) || filepath.Clean(name) != name || name == string(filepath.Separator) || strings.ContainsAny(name, "\x00\r\n") {
		return nil, errors.New("private material requires an absolute clean directory")
	}
	physical, err := filepath.EvalSymlinks(name)
	if err != nil || physical != name {
		return nil, errors.New("private material directory must be physical")
	}
	info, err := os.Lstat(name)
	if err != nil || !info.IsDir() || !Owned(info) || info.Mode().Perm()&0o022 != 0 || private && info.Mode().Perm() != 0o700 {
		return nil, errors.New("private material directory ownership or permissions are invalid")
	}
	root, err := os.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		root.Close()
		return nil, errors.New("private material directory changed while opening")
	}
	return root, nil
}

func ReadPrivate(root *os.Root, name string, max int64) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || !Owned(info) || info.Mode().Perm() != 0o600 || info.Size() <= 0 || info.Size() > max {
		return nil, errors.New("private material must be a bounded owned regular 0600 file")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("private material changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(file, max+1))
	if err != nil || int64(len(raw)) != info.Size() || int64(len(raw)) > max {
		return nil, errors.New("private material is unreadable or changed while reading")
	}
	return raw, nil
}
