package bootstrap

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

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
	job, err := prepareEngineCatalog(context.Background(), cfg, replica, activate, func(context.Context) (*domain.CatalogSnapshot, error) {
		return nil, failure
	})
	require.ErrorIs(t, err, failure)
	require.Nil(t, job)
	job, err = prepareEngineCatalog(context.Background(), cfg, replica, activate, func(ctx context.Context) (*domain.CatalogSnapshot, error) {
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.Greater(t, time.Until(deadline), time.Minute, "catalog timeout must not inherit Python task timeout")
		return &domain.CatalogSnapshot{Revision: 1}, nil
	})
	require.NoError(t, err)
	require.NotNil(t, job)
	snapshot, err := replica.CatalogSnapshot(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 1, snapshot.Revision)
}

func TestStartEngineCatalogRejectsMissingTimer(t *testing.T) {
	close, err := StartEngineCatalog(context.Background(), nil, DefaultEngineApplicationConfig(), nil, nil)
	require.ErrorContains(t, err, "timer")
	require.Nil(t, close)
}
