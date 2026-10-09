// Package privatefiles confines private credential reads and atomic writes to
// regular 0600 files and owned 0700 directories.
package privatefiles

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/mooyang-code/moox/packages/security"
)

func Read(path string, limit int64) ([]byte, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return ReadAt(root, filepath.Base(path), limit)
}

func ReadAt(root *os.Root, name string, limit int64) ([]byte, error) {
	if filepath.Base(name) != name || name == "." || limit <= 0 {
		return nil, errors.New("invalid credential filename or size limit")
	}
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return nil, errors.New("secret must be a regular 0600 file")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) {
		return nil, errors.New("secret file changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || int64(len(raw)) > limit {
		return nil, errors.New("secret file is empty or too large")
	}
	return raw, nil
}

func OpenRoot(dir string) (*os.Root, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		return nil, errors.New("credential output directory must be a regular 0700 directory")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		root.Close()
		return nil, errors.New("credential output directory changed while opening")
	}
	return root, nil
}

func OpenSubRoot(parent *os.Root, name string) (*os.Root, error) {
	if filepath.Base(name) != name || name == "." {
		return nil, errors.New("credential directory must be a single path component")
	}
	if err := parent.Mkdir(name, 0o700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	info, err := parent.Lstat(name)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return nil, errors.New("credential subdirectory must be a regular 0700 directory")
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		root.Close()
		return nil, errors.New("credential subdirectory changed while opening")
	}
	return root, nil
}

func Write(root *os.Root, name string, data []byte) error {
	if filepath.Base(name) != name || name == "." {
		return errors.New("secret filename must be a single path component")
	}
	info, err := root.Lstat(name)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil && (!info.Mode().IsRegular() || info.Mode().Perm() != 0o600) {
		return errors.New("existing secret must be a regular 0600 file")
	}
	id, err := security.RandomHex(16)
	if err != nil {
		return err
	}
	tmp := ".secret-" + id
	file, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer root.Remove(tmp)
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write secret file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return root.Rename(tmp, name)
}
