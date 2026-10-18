package fsutil

import (
	"crypto/rand"
	"errors"
	"io/fs"
	"os"
	"path"
	"strings"
)

// WritePrivate writes into an exclusively owned preparation directory. Every
// parent is checked without following links. Replacement must be explicit and
// accepts only an owned regular file; other objects are always refused.
func WritePrivate(root *os.Root, name string, raw []byte, replace bool) error {
	if !fs.ValidPath(name) || name == "." || strings.ContainsAny(name, "\\\x00\r\n") {
		return errors.New("private write requires a clean relative path")
	}
	directory := path.Dir(name)
	if directory != "." {
		prefix := ""
		for _, part := range strings.Split(directory, "/") {
			prefix = path.Join(prefix, part)
			if err := root.Mkdir(prefix, 0o700); err != nil && !os.IsExist(err) {
				return err
			}
			info, err := root.Lstat(prefix)
			if err != nil || !info.IsDir() || !Owned(info) || info.Mode().Perm() != 0o700 {
				return errors.New("private write parent must be an owned physical 0700 directory")
			}
		}
	}
	if info, err := root.Lstat(name); err == nil {
		if !replace || !info.Mode().IsRegular() || !Owned(info) {
			return errors.New("private write refuses an existing destination")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	temporary := path.Join(directory, ".private-"+rand.Text())
	file, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer root.Remove(temporary)
	chmodErr := file.Chmod(0o600)
	_, writeErr := file.Write(raw)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(chmodErr, writeErr, syncErr, closeErr); err != nil {
		return err
	}
	if replace {
		if err := root.Rename(temporary, name); err != nil {
			return err
		}
	} else {
		parent, err := root.Open(directory)
		if err != nil {
			return err
		}
		err = RenameExclusive(parent, path.Base(temporary), path.Base(name))
		parent.Close()
		if err != nil {
			return err
		}
	}
	parent, err := root.Open(directory)
	if err != nil {
		return err
	}
	defer parent.Close()
	return parent.Sync()
}
