package inputcache

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestManagerRejectsReadOnlyDirectoryAtStartup(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can create files in read-only-mode directories")
	}
	cfg := DefaultConfig()
	cfg.Dir = t.TempDir()
	require.NoError(t, os.Chmod(cfg.Dir, 0o500))
	t.Cleanup(func() { require.NoError(t, os.Chmod(cfg.Dir, 0o700)) })
	m, err := NewManager(cfg)
	require.Nil(t, m)
	require.ErrorContains(t, err, "not writable")
}

func TestManagerStartupProbeLeavesNoFiles(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Dir = t.TempDir()
	m, err := NewManager(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, m.Close()) })
	files, err := os.ReadDir(m.ownership.session)
	require.NoError(t, err)
	require.Len(t, files, 1)
	require.Equal(t, cacheOwnerName, files[0].Name())
	require.NoError(t, m.Close())
	files, err = os.ReadDir(cfg.Dir)
	require.NoError(t, err)
	require.Len(t, files, 1)
	require.Equal(t, cacheLockName, files[0].Name())
}

func testManager(t *testing.T) *Manager {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Dir = t.TempDir()
	cfg.MaxBytes = 1 << 20
	cfg.MinFreeBytes = 1
	cfg.RebuildKeepRows = 1
	m, err := NewManager(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, m.Close()) })
	return m
}

func TestManagerContractReplacementRejectsStaleHandles(t *testing.T) {
	m := testManager(t)
	ctx := context.Background()
	key := SourceKey{SpaceID: "space/../../", ViewID: "view"}
	columns := []Column{{"id", "VARCHAR"}}
	h, err := m.Get(ctx, key, "v1", columns, []string{"id"})
	require.NoError(t, err)
	oldDir := h.source.generation.dir
	require.NoError(t, h.Fill(func(db *Database, _ uint64) error { return db.Upsert(ctx, [][]any{{"BTC"}}, time.Now()) }))
	columns[0].Type = "BIGINT"
	_, err = m.Get(ctx, key, "v1", columns, []string{"id"})
	require.ErrorContains(t, err, "immutable contract")
	next, err := m.Get(ctx, key, "v2", []Column{{"id", "VARCHAR"}, {"value", "DOUBLE"}}, []string{"id"})
	require.NoError(t, err)
	_, err = os.Stat(oldDir)
	require.True(t, os.IsNotExist(err))
	for _, use := range []func(func(*Database, uint64) error) error{h.Use, h.Fill} {
		require.ErrorIs(t, use(func(*Database, uint64) error { t.Fatal("stale callback ran"); return nil }), ErrStaleHandle)
	}
	require.NoError(t, next.Use(func(db *Database, _ uint64) error {
		var count int
		err := db.db.QueryRow("SELECT count(*) FROM cached_rows").Scan(&count)
		require.Zero(t, count)
		return err
	}))
}

func TestManagerCapacityPausesAndResumesWithoutDeletingOrphans(t *testing.T) {
	m := testManager(t)
	ctx := context.Background()
	key := SourceKey{"s", "v"}
	columns := []Column{{"id", "VARCHAR"}}
	h, err := m.Get(ctx, key, "v1", columns, []string{"id"})
	require.NoError(t, err)
	padding := filepath.Join(m.cfg.Dir, "orphan-from-previous-process")
	require.NoError(t, os.WriteFile(padding, make([]byte, 2<<20), 0o600))
	result, err := m.Maintain(ctx)
	require.NoError(t, err)
	require.True(t, result.PauseWrites)
	require.Greater(t, result.Bytes, m.cfg.MaxBytes)
	require.FileExists(t, padding)
	require.ErrorIs(t, h.Use(func(*Database, uint64) error { return nil }), ErrStaleHandle)
	h, err = m.Get(ctx, key, "v1", columns, []string{"id"})
	require.NoError(t, err)
	require.NoError(t, h.Use(func(*Database, uint64) error { return nil }))
	require.ErrorIs(t, h.Fill(func(*Database, uint64) error { t.Fatal("paused fill ran"); return nil }), ErrWritesPaused)
	require.NoError(t, os.Remove(padding))
	result, err = m.Maintain(ctx)
	require.NoError(t, err)
	require.False(t, result.PauseWrites)
	require.NoError(t, h.Fill(func(db *Database, _ uint64) error { return db.Upsert(ctx, [][]any{{"BTC"}}, time.Now()) }))
}

func TestManagerUseFailsFastAndCloseDrains(t *testing.T) {
	m := testManager(t)
	h, err := m.Get(context.Background(), SourceKey{"s", "v"}, "v1", []Column{{"id", "VARCHAR"}}, []string{"id"})
	require.NoError(t, err)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() { done <- h.Use(func(*Database, uint64) error { close(entered); <-release; return nil }) }()
	<-entered
	_, err = m.Maintain(context.Background())
	require.ErrorIs(t, err, ErrCacheBusy)
	closed := make(chan error, 1)
	go func() { closed <- m.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("close did not drain active callback: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	_, err = NewManager(m.cfg)
	require.ErrorIs(t, err, ErrCacheLocked, "lock released before active callback drained")
	close(release)
	require.NoError(t, <-done)
	require.NoError(t, <-closed)
	require.ErrorIs(t, h.Use(func(*Database, uint64) error { return nil }), ErrCacheClosed)
	require.NoError(t, m.Close())
}

func TestManagerPausedSchemaChangeRetiresOldGeneration(t *testing.T) {
	m := testManager(t)
	ctx := context.Background()
	key := SourceKey{"s", "v"}
	columns := []Column{{"id", "VARCHAR"}}
	h, err := m.Get(ctx, key, "v1", columns, []string{"id"})
	require.NoError(t, err)
	m.paused.Store(true)
	_, err = m.Get(ctx, key, "v2", columns, []string{"id"})
	require.ErrorIs(t, err, ErrWritesPaused)
	require.ErrorIs(t, h.Use(func(*Database, uint64) error { return nil }), ErrStaleHandle)
	_, err = m.Maintain(ctx)
	require.NoError(t, err)
	_, err = m.Get(ctx, key, "v2", columns, []string{"id"})
	require.NoError(t, err)
}

func TestManagerFailedSchemaReplacementRetiresOldGeneration(t *testing.T) {
	m := testManager(t)
	ctx := context.Background()
	key := SourceKey{"s", "v"}
	h, err := m.Get(ctx, key, "v1", []Column{{"id", "VARCHAR"}}, []string{"id"})
	require.NoError(t, err)
	oldDir := h.source.generation.dir
	_, err = m.Get(ctx, key, "v2", []Column{{"id", "UNSUPPORTED_TYPE"}}, []string{"id"})
	require.ErrorContains(t, err, "unsupported cache column type")
	for _, use := range []func(func(*Database, uint64) error) error{h.Use, h.Fill} {
		require.ErrorIs(t, use(func(*Database, uint64) error { t.Fatal("retired callback ran"); return nil }), ErrStaleHandle)
	}
	_, err = os.Stat(oldDir)
	require.True(t, os.IsNotExist(err))
	_, err = m.Get(ctx, key, "v2", []Column{{"id", "VARCHAR"}}, []string{"id"})
	require.NoError(t, err)
}

func TestManagerMaintenanceGateAndValidation(t *testing.T) {
	_, err := NewManager(Config{})
	require.Error(t, err)
	m := testManager(t)
	h, err := m.Get(context.Background(), SourceKey{"s", "v"}, "v1", []Column{{"id", "VARCHAR"}}, []string{"id"})
	require.NoError(t, err)
	// The same exclusive gate is held throughout real capacity maintenance.
	m.mu.Lock()
	err = h.Use(func(*Database, uint64) error { t.Fatal("maintenance admitted read"); return nil })
	require.ErrorIs(t, err, ErrCacheBusy)
	err = h.Fill(func(*Database, uint64) error { t.Fatal("maintenance admitted write"); return nil })
	require.ErrorIs(t, err, ErrCacheBusy)
	m.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = m.Maintain(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, h.Fill(func(*Database, uint64) error { return nil }), ErrWritesPaused)
	_, err = m.Maintain(context.Background())
	require.NoError(t, err)
	require.NoError(t, h.Fill(func(*Database, uint64) error { return nil }))
}

func TestManagerStartupCountsExistingFilesAndRejectsSymlinks(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Dir = t.TempDir()
	cfg.MaxBytes = 1
	file := filepath.Join(cfg.Dir, "unowned")
	require.NoError(t, os.WriteFile(file, []byte("orphan"), 0o600))
	m, err := NewManager(cfg)
	require.NoError(t, err)
	require.True(t, m.paused.Load())
	require.NoError(t, m.Close())
	require.FileExists(t, file)
	require.NoError(t, os.Symlink(file, filepath.Join(cfg.Dir, "link")))
	_, err = NewManager(cfg)
	require.ErrorContains(t, err, "symlink")
}
