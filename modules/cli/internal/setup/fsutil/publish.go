package fsutil

import (
	"context"
	"crypto/rand"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// PublishPrivate writes a complete new 0700 directory with 0600 payloads.
// It refuses every existing destination, including symbolic links. A true
// result on error means the rename succeeded but syncing the parent failed.
func PublishPrivate(ctx context.Context, destination string, files map[string][]byte) (published bool, returnErr error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !filepath.IsAbs(destination) || filepath.Clean(destination) != destination || destination == string(filepath.Separator) || strings.ContainsAny(destination, "\x00\r\n") || len(files) == 0 {
		return false, errors.New("private publication requires a new absolute directory and payloads")
	}
	for name := range files {
		if !fs.ValidPath(name) || name == "." || strings.ContainsAny(name, "\\\x00\r\n") {
			return false, errors.New("private publication contains an invalid relative path")
		}
	}
	parent, err := OpenPhysicalRoot(filepath.Dir(destination), false)
	if err != nil {
		return false, err
	}
	defer parent.Close()
	name := filepath.Base(destination)
	if _, err := parent.Lstat(name); !os.IsNotExist(err) {
		return false, errors.New("private publication never replaces an existing destination")
	}
	stageName := ".material-" + rand.Text()
	if err := parent.Mkdir(stageName, 0o700); err != nil {
		return false, err
	}
	defer func() {
		if err := parent.RemoveAll(stageName); err != nil {
			returnErr = errors.Join(returnErr, errors.New("could not remove unpublished private directory"))
		}
	}()
	stage, err := parent.OpenRoot(stageName)
	if err != nil {
		return false, err
	}
	defer stage.Close()
	directories := map[string]bool{".": true}
	for name, raw := range files {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		directory := path.Dir(name)
		if err := stage.MkdirAll(filepath.FromSlash(directory), 0o700); err != nil {
			return false, err
		}
		for dir := directory; dir != "."; dir = path.Dir(dir) {
			directories[dir] = true
		}
		file, err := stage.OpenFile(filepath.FromSlash(name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return false, err
		}
		_, writeErr := file.Write(raw)
		syncErr := file.Sync()
		closeErr := file.Close()
		if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
			return false, err
		}
	}
	for name := range directories {
		directory, err := stage.Open(filepath.FromSlash(name))
		if err != nil {
			return false, err
		}
		syncErr, closeErr := directory.Sync(), directory.Close()
		if err := errors.Join(syncErr, closeErr); err != nil {
			return false, err
		}
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	directory, err := parent.Open(".")
	if err != nil {
		return false, err
	}
	defer directory.Close()
	if err := RenameExclusive(directory, stageName, name); err != nil {
		return false, err
	}
	if err := directory.Sync(); err != nil {
		return true, errors.New("private directory was published but parent sync failed")
	}
	return true, nil
}
