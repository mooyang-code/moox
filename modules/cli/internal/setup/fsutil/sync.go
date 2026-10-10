package fsutil

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"slices"
)

// SyncPrivateTree persists files before directories, deepest directories first.
// Callers hold exclusive ownership and have closed every state writer.
func SyncPrivateTree(ctx context.Context, root *os.Root) error {
	var directories []string
	err := fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil || !Owned(info) || !(info.IsDir() && info.Mode().Perm() == 0o700 || info.Mode().IsRegular() && info.Mode().Perm() == 0o600 && SingleLink(info)) {
			return errors.New("durable checkpoint requires owned private physical files and directories")
		}
		if info.IsDir() {
			directories = append(directories, name)
			return nil
		}
		file, err := root.Open(name)
		if err != nil {
			return err
		}
		return errors.Join(file.Sync(), file.Close())
	})
	if err != nil {
		return err
	}
	slices.Reverse(directories)
	for _, name := range directories {
		file, err := root.Open(name)
		if err != nil {
			return err
		}
		if err := errors.Join(file.Sync(), file.Close()); err != nil {
			return err
		}
	}
	return nil
}
