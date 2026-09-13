package bootstrap

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/catalogsync"
	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	"github.com/stretchr/testify/require"
)

func TestPrepareEngineCatalogRequiresStartupSync(t *testing.T) {
	replica, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "replica.db")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, replica.Close()) })
	require.NoError(t, replica.ApplySchema(factorschema.AllSQL()))
	cfg := DefaultEngineApplicationConfig()
	cfg.Engine.FactorsDir = t.TempDir()
	cfg.Engine.TaskTimeoutMS = 1
	activate := func(_ context.Context, _, _ domain.CatalogSnapshot, commit func() error) error { return commit() }
	failure := errors.New("control unavailable")
	job, stop, err := prepareEngineCatalog(context.Background(), cfg, replica, activate, func(context.Context) (*domain.CatalogSnapshot, error) {
		return nil, failure
	})
	require.ErrorIs(t, err, failure)
	require.Nil(t, job)
	require.Nil(t, stop)
	job, stop, err = prepareEngineCatalog(context.Background(), cfg, replica, activate, func(ctx context.Context) (*domain.CatalogSnapshot, error) {
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.Greater(t, time.Until(deadline), time.Minute, "catalog timeout must not inherit Python task timeout")
		return &domain.CatalogSnapshot{Revision: 1}, nil
	})
	require.NoError(t, err)
	t.Cleanup(stop)
	require.NotNil(t, job)
	snapshot, err := replica.CatalogSnapshot(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 1, snapshot.Revision)
}

func TestCatalogLifecycleCancelsAndDrainsBeforeStopReturns(t *testing.T) {
	lifecycle := newCatalogLifecycle(context.Background())
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		_, err := lifecycle.run(context.Background(), func(ctx context.Context) (catalogsync.SyncResult, error) {
			close(entered)
			<-ctx.Done()
			close(canceled)
			<-release
			return catalogsync.SyncResult{}, ctx.Err()
		})
		finished <- err
	}()
	<-entered
	stopped := make(chan struct{})
	go func() { lifecycle.stop(); close(stopped) }()
	<-canceled
	select {
	case <-stopped:
		t.Fatal("stop returned before active callback drained")
	default:
	}
	_, err := lifecycle.run(context.Background(), func(context.Context) (catalogsync.SyncResult, error) {
		t.Error("callback admitted after stop")
		return catalogsync.SyncResult{}, nil
	})
	require.ErrorIs(t, err, context.Canceled)
	close(release)
	require.ErrorIs(t, <-finished, context.Canceled)
	<-stopped
	lifecycle.stop()
}

func TestCatalogLifecycleRejectsCanceledEngineContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	lifecycle := newCatalogLifecycle(ctx)
	defer lifecycle.stop()
	cancel()
	_, err := lifecycle.run(context.Background(), func(context.Context) (catalogsync.SyncResult, error) {
		t.Fatal("callback admitted after engine cancellation")
		return catalogsync.SyncResult{}, nil
	})
	require.ErrorIs(t, err, context.Canceled)
}

func TestStartEngineCatalogRejectsMissingTimer(t *testing.T) {
	close, err := StartEngineCatalog(context.Background(), nil, DefaultEngineApplicationConfig(), nil, nil)
	require.ErrorContains(t, err, "timer")
	require.Nil(t, close)
}
