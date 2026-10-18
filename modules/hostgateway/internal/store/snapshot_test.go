package store

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/mooyang-code/moox/modules/hostgateway/internal/snapshot"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/testsnapshot"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestCompleteSnapshotPersistenceAndCorruption(t *testing.T) {
	cache := NewSnapshots(filepath.Join(t.TempDir(), "cache"), "storage")
	view, err := snapshot.Build("storage", testsnapshot.New(t, "storage", "storage-primary"))
	require.NoError(t, err)
	require.NoError(t, cache.Save(view))
	loaded, err := cache.Load()
	require.NoError(t, err)
	require.True(t, proto.Equal(view.Proto(), loaded.Proto()), "routes, directory and every verifier key must survive restart")
	require.NoError(t, cache.Check())
	if runtime.GOOS != "windows" {
		info, err := os.Stat(cache.Path())
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		info, err = os.Stat(filepath.Dir(cache.Path()))
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	}
	raw := view.Proto()
	raw.VerificationKeys[0].Secret[0] ^= 1
	encoded, err := proto.Marshal(raw)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cache.Path(), encoded, 0o600))
	_, err = cache.Load()
	require.ErrorIs(t, err, snapshot.ErrInvalid)
	require.Error(t, cache.Check())
	require.NoError(t, cache.Save(view))
	files, err := filepath.Glob(filepath.Join(filepath.Dir(cache.Path()), ".snapshot-*"))
	require.NoError(t, err)
	require.Empty(t, files)
	wrong := NewSnapshots(filepath.Join(t.TempDir(), "wrong"), "control")
	require.Error(t, wrong.Save(view))
}

func TestSnapshotPrivateFileAndDirectoryContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission/symlink contract; Windows uses deployment ACLs")
	}
	for _, kind := range []string{"directory mode", "directory link", "file mode", "file link", "file directory", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			parent := t.TempDir()
			path := filepath.Join(parent, "cache")
			cache := NewSnapshots(path, "storage")
			view, err := snapshot.Build("storage", testsnapshot.New(t, "storage"))
			require.NoError(t, err)
			require.NoError(t, cache.Save(view))
			switch kind {
			case "directory mode":
				require.NoError(t, os.Chmod(path, 0o755))
			case "directory link":
				require.NoError(t, os.Rename(path, path+"-real"))
				require.NoError(t, os.Symlink(path+"-real", path))
			case "file mode":
				require.NoError(t, os.Chmod(cache.Path(), 0o644))
			case "file link":
				require.NoError(t, os.Rename(cache.Path(), cache.Path()+"-real"))
				require.NoError(t, os.Symlink(cache.Path()+"-real", cache.Path()))
			case "file directory":
				require.NoError(t, os.Remove(cache.Path()))
				require.NoError(t, os.Mkdir(cache.Path(), 0o700))
			case "oversize":
				f, err := os.OpenFile(cache.Path(), os.O_WRONLY, 0o600)
				require.NoError(t, err)
				require.NoError(t, f.Truncate(snapshot.MaxBytes+1))
				require.NoError(t, f.Close())
			}
			_, err = cache.Load()
			require.Error(t, err)
			if kind != "oversize" {
				require.Error(t, cache.Save(view))
			}
		})
	}
}
