package inputcache

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCacheOwnershipSubprocessHelper(t *testing.T) {
	root := os.Getenv("MOOX_CACHE_OWNERSHIP_HELPER")
	if root == "" {
		return
	}
	cfg := DefaultConfig()
	cfg.Dir = root
	m, err := NewManager(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	_, err = m.Get(context.Background(), SourceKey{"s", "v"}, "contract", []Column{{"id", "VARCHAR"}}, []string{"id"})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	fmt.Println(m.ownership.session)
	_, _ = io.Copy(io.Discard, os.Stdin)
	// Deliberately bypass Close to exercise process-death lock release/recovery.
	os.Exit(0)
}

func TestCacheOwnershipCrossProcessLockAndCrashRecovery(t *testing.T) {
	root := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCacheOwnershipSubprocessHelper$")
	cmd.Env = append(os.Environ(), "MOOX_CACHE_OWNERSHIP_HELPER="+root)
	output, err := cmd.StdoutPipe()
	require.NoError(t, err)
	input, err := cmd.StdinPipe()
	require.NoError(t, err)
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = input.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	scanner := bufio.NewScanner(output)
	require.True(t, scanner.Scan(), "child did not acquire cache ownership")
	oldSession := scanner.Text()
	require.True(t, strings.HasPrefix(oldSession, root+string(os.PathSeparator)))
	cfg := DefaultConfig()
	cfg.Dir = root
	_, err = NewManager(cfg)
	require.ErrorIs(t, err, ErrCacheLocked)
	require.NoError(t, cmd.Process.Kill())
	require.Error(t, cmd.Wait())
	m, err := NewManager(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, m.Close()) })
	_, err = os.Stat(oldSession)
	require.True(t, os.IsNotExist(err), "owned crashed generation was not recovered")
	require.NotEqual(t, oldSession, m.ownership.session)
	require.NoError(t, m.Close())
	next, err := NewManager(cfg)
	require.NoError(t, err)
	require.NoError(t, next.Close())
}

func TestCacheOwnershipPreservesUnmarkedFilesAndDirectories(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Dir = t.TempDir()
	unmarked := filepath.Join(cfg.Dir, cacheSessionPrefix+strings.Repeat("a", 32))
	require.NoError(t, os.Mkdir(unmarked, 0o700))
	file := filepath.Join(unmarked, "important")
	require.NoError(t, os.WriteFile(file, []byte("preserve"), 0o600))
	m, err := NewManager(cfg)
	require.NoError(t, err)
	_, err = NewManager(cfg)
	require.ErrorIs(t, err, ErrCacheLocked)
	require.NoError(t, m.Close())
	require.FileExists(t, file)
	require.FileExists(t, filepath.Join(cfg.Dir, cacheLockName))
}

func TestCacheOwnershipInterruptedMarkerPublicationRemainsUnmarked(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Dir = t.TempDir()
	path := filepath.Join(cfg.Dir, cacheSessionPrefix+strings.Repeat("c", 32))
	require.NoError(t, os.Mkdir(path, 0o700))
	pending := filepath.Join(path, ".owner.pending")
	require.NoError(t, os.WriteFile(pending, []byte(`{"owner":`), 0o600))
	m, err := NewManager(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, m.Close()) })
	require.FileExists(t, pending)
	_, err = os.Stat(filepath.Join(path, cacheOwnerName))
	require.True(t, os.IsNotExist(err))
	bytes, err := DirectoryBytes(cfg.Dir)
	require.NoError(t, err)
	require.GreaterOrEqual(t, bytes, int64(len(`{"owner":`)))
	require.NoError(t, m.Close())
	require.FileExists(t, pending)
}

func TestCacheOwnershipRejectsUnknownMarkersAndSymlinks(t *testing.T) {
	for _, kind := range []string{"unknown-version", "wrong-session", "symlink", "partial-marker", "unknown-file"} {
		t.Run(kind, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Dir = t.TempDir()
			name := cacheSessionPrefix + strings.Repeat("b", 32)
			path := filepath.Join(cfg.Dir, name)
			require.NoError(t, os.Mkdir(path, 0o700))
			marker := cacheOwnerMarker{Owner: "moox-factor-inputcache", Version: 1, Session: name}
			if kind == "unknown-version" {
				marker.Version = 2
			}
			if kind == "wrong-session" {
				marker.Session = "other"
			}
			data, err := json.Marshal(marker)
			require.NoError(t, err)
			if kind == "partial-marker" {
				data = []byte("{")
			}
			require.NoError(t, os.WriteFile(filepath.Join(path, cacheOwnerName), data, 0o600))
			if kind == "symlink" {
				require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(path, "unsafe")))
			}
			if kind == "unknown-file" {
				require.NoError(t, os.WriteFile(filepath.Join(path, "important"), []byte("preserve"), 0o600))
			}
			_, err = NewManager(cfg)
			require.Error(t, err)
			require.DirExists(t, path)
			// A startup rejection must release the lifetime lock.
			require.NoError(t, os.RemoveAll(path))
			m, err := NewManager(cfg)
			require.NoError(t, err)
			require.NoError(t, m.Close())
		})
	}
}

func TestCacheOwnershipRejectsLockSymlink(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Dir = t.TempDir()
	target := filepath.Join(t.TempDir(), "target")
	require.NoError(t, os.WriteFile(target, []byte("preserve"), 0o600))
	require.NoError(t, os.Symlink(target, filepath.Join(cfg.Dir, cacheLockName)))
	_, err := NewManager(cfg)
	require.Error(t, err)
	data, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "preserve", string(data))
}
