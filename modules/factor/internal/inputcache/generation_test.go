package inputcache

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGenerationReplacementResetsDataAndRetiresOldFile(t *testing.T) {
	ctx := context.Background()
	g, err := NewGeneration(ctx, t.TempDir(), []Column{{"id", "VARCHAR"}}, []string{"id"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, g.Close()) })
	oldDir := g.dir
	require.NoError(t, g.Use(func(db *Database, epoch uint64) error {
		require.EqualValues(t, 1, epoch)
		return db.Upsert(ctx, [][]any{{"BTC"}}, time.Now())
	}))
	require.NoError(t, g.ReplaceSchema(ctx, []Column{{"id", "VARCHAR"}, {"new_field", "DOUBLE"}}, []string{"id"}))
	_, err = os.Stat(oldDir)
	require.True(t, os.IsNotExist(err))
	require.NoError(t, g.Use(func(db *Database, epoch uint64) error {
		require.EqualValues(t, 2, epoch)
		var count int
		if err := db.db.QueryRowContext(ctx, "SELECT count(*) FROM cached_rows").Scan(&count); err != nil {
			return err
		}
		require.Zero(t, count)
		return nil
	}))
	require.Error(t, g.Rebuild(ctx, 0))
	require.NoError(t, g.Use(func(_ *Database, epoch uint64) error {
		require.EqualValues(t, 2, epoch)
		return nil
	}))
}

func TestGenerationMaintenanceWaitIsCancellableAndUsersFailFast(t *testing.T) {
	ctx := context.Background()
	g, err := NewGeneration(ctx, t.TempDir(), []Column{{"id", "VARCHAR"}}, []string{"id"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, g.Close()) })
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- g.Use(func(db *Database, _ uint64) error {
			close(entered)
			<-release
			return db.Upsert(ctx, [][]any{{"BTC"}}, time.Now())
		})
	}()
	<-entered
	require.ErrorIs(t, g.Use(func(*Database, uint64) error { return nil }), ErrCacheBusy)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	rebuildErr := g.Rebuild(cancelled, 1)
	close(release)
	require.NoError(t, <-done)
	require.ErrorIs(t, rebuildErr, context.Canceled)
	require.NoError(t, g.Use(func(db *Database, epoch uint64) error {
		require.EqualValues(t, 1, epoch)
		var id string
		err := db.db.QueryRowContext(ctx, "SELECT id FROM cached_rows").Scan(&id)
		require.Equal(t, "BTC", id)
		return err
	}))
	require.NoError(t, g.Rebuild(ctx, 1))
	require.NoError(t, g.Use(func(db *Database, epoch uint64) error {
		require.EqualValues(t, 2, epoch)
		var id string
		return db.db.QueryRowContext(ctx, "SELECT id FROM cached_rows").Scan(&id)
	}))
}
