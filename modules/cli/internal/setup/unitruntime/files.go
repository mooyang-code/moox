package unitruntime

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

func privateDirectory(parent *os.Root, name string) (*os.Root, error) {
	if filepath.Base(name) != name || name == "." {
		return nil, errors.New("invalid runtime state directory")
	}
	if err := parent.Mkdir(name, 0o700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	info, err := parent.Lstat(name)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 || !ownerMatches(info) {
		return nil, errors.New("runtime state requires owned regular 0700 directories")
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		root.Close()
		return nil, errors.New("runtime state directory changed while opening")
	}
	return root, nil
}

func syncDirectory(root *os.Root) error {
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func writeState(root *os.Root, name string, value any) error {
	if filepath.Base(name) != name || name == "." {
		return errors.New("invalid runtime state filename")
	}
	if info, err := root.Lstat(name); err == nil && (!info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !ownerMatches(info)) {
		return errors.New("existing runtime state must be a regular 0600 file")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	nameTemp := ".state-" + hex.EncodeToString(id[:])
	file, err := root.OpenFile(nameTemp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	defer root.Remove(nameTemp)
	if _, err := file.Write(append(raw, '\n')); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := root.Rename(nameTemp, name); err != nil {
		return err
	}
	return syncDirectory(root)
}

func removeState(root *os.Root, name string) error {
	if err := root.Remove(name); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncDirectory(root)
}

func stateExists(root *os.Root, name string) (bool, error) {
	info, err := root.Lstat(name)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !ownerMatches(info) {
		return false, errors.New("runtime marker must be a regular 0600 file")
	}
	return true, nil
}
