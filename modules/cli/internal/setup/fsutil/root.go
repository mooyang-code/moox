package fsutil

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// TightenPrivate 把属于当前用户、别人不可写的目录收紧到 0700。其他情况不动目录，交给 OpenPhysicalRoot 报错。
func TightenPrivate(name string) error {
	info, err := os.Lstat(name)
	if err != nil || !info.IsDir() || !Owned(info) || info.Mode().Perm()&0o022 != 0 || info.Mode().Perm() == 0o700 {
		return nil
	}
	return os.Chmod(name, 0o700)
}

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
		return nil, fmt.Errorf("private material directory ownership or permissions are invalid: %s 要属于当前用户且不能被其他人写入，私有目录权限必须是 700（chmod 700 %s）", name, name)
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
