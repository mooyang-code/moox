package bootstrap

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	"github.com/stretchr/testify/require"
)

func controlTestConfig(t *testing.T) *ControlConfig {
	t.Helper()
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "control-test")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "control-test-secret")
	cfg := DefaultControlConfig()
	cfg.Database.Path = filepath.Join(t.TempDir(), "catalog.db")
	cfg.Storage.GatewayNodeID = "storage-test"
	cfg.ArtifactsDir = filepath.Join(t.TempDir(), "artifacts")
	return cfg
}

func TestOpenControlResourcesValidatesBeforeOpening(t *testing.T) {
	cfg := controlTestConfig(t)
	_, err := OpenControlResources(nil, cfg)
	require.Error(t, err)
	_, err = OpenControlResources(context.Background(), nil)
	require.Error(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = OpenControlResources(ctx, cfg)
	require.ErrorIs(t, err, context.Canceled)
	cfg.ArtifactsDir = " "
	_, err = OpenControlResources(context.Background(), cfg)
	require.ErrorContains(t, err, "artifacts_dir")
	_, err = os.Stat(cfg.Database.Path)
	require.True(t, os.IsNotExist(err))
}

func TestOpenControlResourcesRejectsUnsignableGatewayNode(t *testing.T) {
	for _, node := range []string{"", " ", "storage\nnode"} {
		cfg := controlTestConfig(t)
		cfg.Storage.GatewayNodeID = node
		cfg.Storage.HMACKeyFile = filepath.Join(t.TempDir(), "must-not-read-missing-key")
		_, err := OpenControlResources(context.Background(), cfg)
		require.ErrorContains(t, err, "gateway target node")
		_, err = os.Stat(cfg.Database.Path)
		require.True(t, os.IsNotExist(err))
	}
}

func TestControlResourcesLifecycle(t *testing.T) {
	cfg := controlTestConfig(t)
	t.Setenv("MOOX_FACTOR_ENGINE_PYTHON_BIN", filepath.Join(t.TempDir(), "missing-python"))
	t.Setenv("MOOX_FACTOR_ENGINE_WORKER_PATH", filepath.Join(t.TempDir(), "missing-worker.py"))
	cfg.EventBus.URLs = []string{"nats://127.0.0.1:1"}
	r, err := OpenControlResources(context.Background(), cfg)
	require.NoError(t, err)
	require.NotNil(t, r.Registry)
	require.NotNil(t, r.Metadata)
	require.NotNil(t, r.Store.Factors())
	require.NotNil(t, r.Store.Bindings())
	snapshot, err := r.Store.CatalogSnapshot(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 1, snapshot.Revision)
	r.Cancel()
	require.ErrorIs(t, r.Context().Err(), context.Canceled)
	require.NoError(t, r.Store.Ping(context.Background()))
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- r.Close() }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Error(t, r.Store.Ping(context.Background()))
}

func TestControlResourcesRestoresAndValidatesPersistedArtifacts(t *testing.T) {
	for _, valid := range []bool{true, false} {
		t.Run(fmt.Sprint(valid), func(t *testing.T) {
			cfg := controlTestConfig(t)
			db, err := store.Open(&store.Options{Path: cfg.Database.Path})
			require.NoError(t, err)
			require.NoError(t, db.ApplySchema(factorschema.AllSQL()))
			source := "def compute(df, params, context): return {}\n"
			hash := fmt.Sprintf("%x", sha256.Sum256([]byte(source)))
			if !valid {
				hash = "wrong-hash"
			}
			require.NoError(t, db.Factors().Create(context.Background(), domain.FactorDef{FactorID: "f", Name: "factor", FactorType: domain.FactorTypeTimeSeries, SourceCode: source, SourceHash: hash, InputColumns: []string{}, Outputs: []string{}, ParamsJSON: "{}", LookbackPeriods: 1, Status: domain.FactorStatusDisabled}))
			require.NoError(t, db.Close())
			r, err := OpenControlResources(context.Background(), cfg)
			if !valid {
				require.ErrorContains(t, err, "source hash mismatch")
				require.Nil(t, r)
				reopened, openErr := store.Open(&store.Options{Path: cfg.Database.Path})
				require.NoError(t, openErr)
				require.NoError(t, reopened.ApplySchema(factorschema.AllSQL()))
				factor, getErr := reopened.Factors().Get(context.Background(), "f")
				require.NoError(t, getErr)
				factor.SourceHash = fmt.Sprintf("%x", sha256.Sum256([]byte(source)))
				require.NoError(t, reopened.Factors().Update(context.Background(), *factor))
				require.NoError(t, reopened.Close())
				recovered, retryErr := OpenControlResources(context.Background(), cfg)
				require.NoError(t, retryErr)
				require.NoError(t, recovered.Close())
				return
			}
			require.NoError(t, err)
			defer r.Close()
			factor, err := r.Store.Factors().Get(context.Background(), "f")
			require.NoError(t, err)
			raw, err := os.ReadFile(factor.SourcePath)
			require.NoError(t, err)
			require.Equal(t, source, string(raw))
		})
	}
}
