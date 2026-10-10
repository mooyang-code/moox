package unitinstall

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
)

func lifecycleOrder(ids []string) []string {
	order := []string{"admin", "eventbus", "host-gateway", "host-agent", "storage-primary", "storage-node", "storage-view", "egress-proxy", "access", "trade", "monitor", "collector", "cloudnode", "factor-mgr", "strategy", "web-host", "console-proxy"}
	result := slices.Clone(ids)
	slices.SortStableFunc(result, func(a, b string) int { return slices.Index(order, a) - slices.Index(order, b) })
	return result
}

func mutablePaths(prepared Prepared, logs bool) []string {
	paths := []string{"data"}
	for _, id := range prepared.Components {
		paths = append(paths, id+"/data", id+"/var")
		if logs {
			paths = append(paths, id+"/log", id+"/logs")
		}
		if id == "console-proxy" {
			paths = append(paths, id+"/certs")
		}
	}
	slices.Sort(paths)
	return paths
}

// copyState copies stopped service state into a new unpublished candidate.
// SQLite databases/WAL and Pebble files are copied after all old unit writers
// have exited. Files are independent: mutable data is never hard linked.
func copyState(ctx context.Context, source, destination Prepared) error {
	from, err := fsutil.OpenPhysicalRoot(source.Directory, true)
	if err != nil {
		return err
	}
	defer from.Close()
	to, err := fsutil.OpenPhysicalRoot(destination.Directory, true)
	if err != nil {
		return err
	}
	defer to.Close()
	for _, name := range mutablePaths(destination, false) {
		info, err := from.Lstat(name)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.IsDir() || !fsutil.Owned(info) || info.Mode().Perm()&0o022 != 0 {
			return errors.New("source state must be an owned physical directory")
		}
		if _, err := to.Lstat(name); !os.IsNotExist(err) {
			return errors.New("state copy never replaces candidate state")
		}
		// Walk a physical sub-root. Parent component directories have already
		// been checked by the immutable installed-release inventory.
		sub, err := from.OpenRoot(name)
		if err != nil {
			return err
		}
		err = fs.WalkDir(sub.FS(), ".", func(relative string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil || !fsutil.Owned(info) || info.Mode().Perm()&0o022 != 0 {
				return errors.New("source state ownership or permissions are invalid")
			}
			target := path.Join(name, relative)
			if info.IsDir() {
				return to.MkdirAll(target, 0o700)
			}
			if !info.Mode().IsRegular() {
				return errors.New("state copy refuses links and special files")
			}
			input, err := sub.Open(relative)
			if err != nil {
				return err
			}
			defer input.Close()
			opened, err := input.Stat()
			if err != nil || !os.SameFile(info, opened) {
				return errors.New("source state changed while opening")
			}
			output, err := to.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			n, copyErr := io.Copy(output, &contextReader{ctx: ctx, reader: io.LimitReader(input, info.Size()+1)})
			syncErr, closeErr := output.Sync(), output.Close()
			if n != info.Size() {
				copyErr = errors.Join(copyErr, errors.New("source state changed while copying"))
			}
			return errors.Join(copyErr, syncErr, closeErr)
		})
		sub.Close()
		if err != nil {
			return err
		}
	}
	return syncTree(to)
}
