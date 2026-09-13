package inputcache

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRuntimeOwnsDelayedMaintenanceAndDirectoryLifetime(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Dir = t.TempDir()
	cfg.MaxBytes = 1 << 20
	cfg.MinFreeBytes = 1
	now := time.Now()
	r, err := NewRuntime(context.Background(), cfg, func() time.Time { return now })
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, r.Close()) })
	require.NoError(t, os.WriteFile(filepath.Join(cfg.Dir, "unowned.data"), make([]byte, 2<<20), 0o600))
	require.NoError(t, r.Job.Handle(context.Background()))
	require.False(t, r.Manager.paused.Load(), "startup callback must not run maintenance")
	now = now.Add(cfg.CheckInterval)
	_ = r.Job.Handle(context.Background())
	require.True(t, r.Manager.paused.Load(), "due timer must account for all cache-directory bytes")
	require.NoError(t, r.Close())
	now = now.Add(cfg.CheckInterval)
	require.ErrorIs(t, r.Job.Handle(context.Background()), context.Canceled)
	next, err := NewManager(cfg)
	// The old directory lock must be released even though unowned data remains.
	require.NoError(t, err)
	require.NoError(t, next.Close())
}

func TestRuntimeCloseWaitsForCacheReader(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Dir = t.TempDir()
	r, err := NewRuntime(context.Background(), cfg, time.Now)
	require.NoError(t, err)
	handle, err := r.Manager.Get(context.Background(), SourceKey{"s", "v"}, "hash:1", []Column{{"id", "VARCHAR"}}, []string{"id"})
	require.NoError(t, err)
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	done := make(chan error, 1)
	go func() {
		done <- handle.Use(func(*Database, uint64) error { close(entered); <-release; return nil })
	}()
	<-entered
	closed := make(chan error, 2)
	go func() { closed <- r.Close() }()
	go func() { closed <- r.Close() }()
	select {
	case <-r.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("close did not cancel maintenance")
	}
	require.ErrorIs(t, r.maintain(context.Background()), context.Canceled)
	select {
	case <-closed:
		t.Fatal("close returned while reader was active")
	case <-time.After(20 * time.Millisecond):
	}
	release <- struct{}{}
	require.NoError(t, <-done)
	for i := 0; i < 2; i++ {
		select {
		case err := <-closed:
			require.NoError(t, err)
		case <-time.After(time.Second):
			t.Fatal("close did not drain")
		}
	}
}
