//go:build linux

package unitinstall

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitruntime"
)

// Metadata initialization runs only on the independent candidate, after old
// writers stop and state is copied. The helper retains maintenance if its
// coordinator dies, and Linux kills it with the creating process/thread.
func initializeCandidateDatabase(ctx context.Context, guard *unitruntime.Maintenance, candidate Prepared) error {
	if !slices.Contains(candidate.Components, "storage-primary") {
		return nil
	}
	if _, err := unitruntime.LoadPlan(planPath(candidate)); err != nil {
		return err
	}
	root, err := fsutil.OpenPhysicalRoot(candidate.Directory, true)
	if err != nil {
		return err
	}
	defer root.Close()
	raw, err := fsutil.ReadPrivate(root, "secrets/runtime-storage-primary.json", 1<<20)
	if err != nil {
		return err
	}
	var values map[string]string
	if json.Unmarshal(raw, &values) != nil {
		return errors.New("Storage initialization requires its validated private runtime environment")
	}
	if len(values) == 0 || len(values) > 256 {
		return errors.New("Storage initialization environment must be bounded")
	}
	for key, value := range values {
		if !strings.HasPrefix(key, "MOOX_") || len(key) > 128 || strings.ContainsRune(value, '\x00') || strings.ContainsFunc(key, func(r rune) bool { return r != '_' && (r < 'A' || r > 'Z') && (r < '0' || r > '9') }) {
			return errors.New("Storage initialization accepts only private MOOX runtime variables")
		}
	}
	if home := values["MOOX_STORAGE_HOME"]; home != "" && home != "./var/storage" {
		return errors.New("Storage initialization requires the candidate's managed relative data root")
	}
	// Storage CLI applies this root to metadata as well, overriding paths in
	// the YAML. Initialization must never write into an old release or mount.
	values["MOOX_STORAGE_HOME"] = "./var/storage"
	command := exec.CommandContext(ctx, filepath.Join(candidate.Directory, "bin/moox-storage-cli"), "init", "--storage-conf", "config/storage.yaml", "--schema-path", "schema/metadata.sql")
	command.Dir = filepath.Join(candidate.Directory, "storage-primary")
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + candidate.Directory}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		command.Env = append(command.Env, key+"="+values[key])
	}
	command.Stdout, command.Stderr = io.Discard, io.Discard
	return guard.UseLock(ctx, func(lock unitruntime.Options) error {
		fd, err := syscall.Dup(lock.MaintenanceLockFD)
		if err != nil {
			return err
		}
		file := os.NewFile(uintptr(fd), "storage-init-maintenance")
		defer file.Close()
		command.ExtraFiles = []*os.File{file}
		command.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := command.Run(); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errors.New("Storage candidate database initialization failed; private child output omitted")
		}
		// The helper has closed SQLite. Sync its metadata files and directory
		// entries without traversing copied datasets or the host gateway view.
		if err := fs.WalkDir(root.FS(), "storage-primary/var/storage/metadata", func(name string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !fsutil.Owned(info) || (!info.IsDir() && !info.Mode().IsRegular()) {
				return errors.New("Storage metadata must remain owned physical files")
			}
			file, err := root.Open(name)
			if err != nil {
				return err
			}
			return errors.Join(file.Sync(), file.Close())
		}); err != nil {
			return err
		}
		for _, name := range []string{".", "storage-primary", "storage-primary/var", "storage-primary/var/storage"} {
			directory, err := root.Open(name)
			if err != nil {
				return err
			}
			if err := errors.Join(directory.Sync(), directory.Close()); err != nil {
				return err
			}
		}
		return nil
	})
}
