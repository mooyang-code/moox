package bootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/inputcache"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"github.com/stretchr/testify/require"
)

func setEngineStorageSecrets(t *testing.T) {
	t.Helper()
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "primary-test")
	t.Setenv("MOOX_STORAGE_VIEW_AUTH_SECRET", "view-test")
}

func TestOpenEngineResourcesRejectsInvalidInputs(t *testing.T) {
	if _, err := OpenEngineResources(context.Background(), nil); err == nil {
		t.Fatal("accepted nil config")
	}
	if _, err := OpenEngineResources(nil, DefaultEngineApplicationConfig()); err == nil {
		t.Fatal("accepted nil context")
	}
	cfg := DefaultEngineApplicationConfig()
	cfg.Engine.PythonWorkers = 0
	cfg.Storage.GatewayNodeID = "storage-test"
	if _, err := OpenEngineResources(context.Background(), cfg); err == nil {
		t.Fatal("accepted zero workers")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := OpenEngineResources(ctx, DefaultEngineApplicationConfig()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled open: %v", err)
	}
}

func TestOpenEngineResourcesRejectsUnsignableGatewayNode(t *testing.T) {
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "engine-test")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "engine-test-secret")
	t.Setenv("MOOX_GATEWAY_TARGET_NODE", "must-not-mask-invalid-config")
	for _, node := range []string{"", " storage", "storage\nnode", string([]byte{0xff})} {
		cfg := DefaultEngineApplicationConfig()
		cfg.Database.Path = filepath.Join(t.TempDir(), "runtime.db")
		cfg.Storage.GatewayNodeID = node
		cfg.Storage.HMACKeyFile = filepath.Join(t.TempDir(), "must-not-read-missing-key")
		_, err := OpenEngineResources(context.Background(), cfg)
		require.ErrorContains(t, err, "gateway target node")
		_, err = os.Stat(cfg.Database.Path)
		require.True(t, os.IsNotExist(err))
	}
}

func TestOpenEngineResourcesRejectsMissingStorageSecrets(t *testing.T) {
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "engine-test")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "engine-test-secret")
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "")
	t.Setenv("MOOX_STORAGE_VIEW_AUTH_SECRET", "view-test")
	cfg := DefaultEngineApplicationConfig()
	cfg.Database.Path = filepath.Join(t.TempDir(), "runtime.db")
	cfg.Storage.GatewayNodeID = "storage-test"
	_, err := OpenEngineResources(context.Background(), cfg)
	require.ErrorContains(t, err, "engine storage primary and view secrets are required")
}

func TestEngineResourcesStagedFailureAndClose(t *testing.T) {
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "engine-test")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "engine-test-secret")
	setEngineStorageSecrets(t)
	cfg := DefaultEngineApplicationConfig()
	cfg.Database.Path = filepath.Join(t.TempDir(), "runtime.db")
	cfg.Engine.PythonBin = filepath.Join(t.TempDir(), "missing-python")
	cfg.Cache.Dir = t.TempDir()
	cfg.Storage.GatewayNodeID = "storage-test"
	r, err := OpenEngineResources(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.Runner != nil || r.PythonPool != nil || r.Storage == nil || r.OperationGate == nil {
		t.Fatal("incorrect staged resources")
	}
	if _, err := r.Store.CatalogSnapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.StartCompute(); err == nil || !strings.Contains(err.Error(), "catalog has not been activated") {
		t.Fatalf("startup catalog gate: %v", err)
	}
	if _, err := r.Store.ReplaceCatalogSnapshot(context.Background(), domain.CatalogSnapshot{Revision: 1}); err != nil {
		t.Fatal(err)
	}
	if err := r.StartCompute(); err == nil || !strings.Contains(err.Error(), "is not executable") {
		t.Fatalf("Python startup failure: %v", err)
	}
	if r.Runner != nil || r.PythonPool != nil {
		t.Fatal("published partial compute resources")
	}
	r.Cancel()
	if !errors.Is(r.Context().Err(), context.Canceled) {
		t.Fatal("runtime was not canceled")
	}
	// Cancellation must leave the DB alive for external callers to drain.
	if _, err := r.Store.CatalogSnapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.StartCompute(); !errors.Is(err, context.Canceled) {
		t.Fatalf("start after cancellation: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Store.CatalogSnapshot(context.Background()); err == nil {
		t.Fatal("database remains open")
	}
	if err := r.StartCompute(); err == nil {
		t.Fatal("started closed resources")
	}
}

func TestEngineAuthInfoSeparatesIdentitiesAndSecrets(t *testing.T) {
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "primary-test")
	t.Setenv("MOOX_STORAGE_VIEW_AUTH_SECRET", "view-test")
	for _, view := range []bool{false, true} {
		auth := EngineAuthInfo(view)
		secret := "primary-test"
		if view {
			secret = "view-test"
		}
		if auth.AppId != EngineAppID || auth.Operator != EngineAppID || auth.AppKey != mooxsecurity.HMACSHA256Hex(secret, []byte(EngineAppID)) {
			t.Fatalf("unexpected engine identity: %q", auth.AppId)
		}
	}
	t.Setenv("MOOX_STORAGE_VIEW_AUTH_SECRET", "")
	if EngineAuthInfo(true).AppKey != "" {
		t.Fatal("view auth borrowed primary secret")
	}
}

func TestEngineResourcesStartsRealPythonAfterCatalogActivation(t *testing.T) {
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "engine-test")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "engine-test-secret")
	setEngineStorageSecrets(t)
	cfg := DefaultEngineApplicationConfig()
	cfg.Database.Path = filepath.Join(t.TempDir(), "runtime.db")
	cfg.Engine.PythonBin = "python3"
	cfg.Cache.Dir = t.TempDir()
	cfg.Storage.GatewayNodeID = "storage-test"
	cfg.Engine.PythonWorkers = 1
	cfg.Engine.WorkerPath = filepath.Join("..", "..", "pyworker", "worker.py")
	cfg.Engine.FactorsDir = t.TempDir()
	r, err := OpenEngineResources(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, r.Close()) })
	_, err = r.Store.ReplaceCatalogSnapshot(r.Context(), domain.CatalogSnapshot{Revision: 1})
	require.NoError(t, err)
	require.NoError(t, r.StartCompute())
	require.NotNil(t, r.Runner)
	require.True(t, r.PythonPool.Status().Ready)
	pool := r.PythonPool
	require.NoError(t, r.StartCompute())
	require.Same(t, pool, r.PythonPool, "repeated start must not leak another worker pool")
	require.NoError(t, r.Close())
	require.False(t, pool.Status().Ready)
}

func TestEngineResourcesOwnsOptionalCache(t *testing.T) {
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "engine-test")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "engine-test-secret")
	setEngineStorageSecrets(t)
	for _, enabled := range []bool{true, false} {
		cfg := DefaultEngineApplicationConfig()
		cfg.Storage.GatewayNodeID = "storage-test"
		cfg.Database.Path = filepath.Join(t.TempDir(), "runtime.db")
		cfg.Cache.Dir = filepath.Join(t.TempDir(), "cache")
		cfg.Cache.Enabled = enabled
		r, err := OpenEngineResources(context.Background(), cfg)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, r.Close()) })
		if enabled {
			require.NotNil(t, r.Cache)
			_, err = inputcache.NewManager(cfg.Cache)
			require.ErrorIs(t, err, inputcache.ErrCacheLocked)
		} else {
			require.Nil(t, r.Cache)
			_, err = os.Stat(cfg.Cache.Dir)
			require.True(t, os.IsNotExist(err))
		}
		require.NoError(t, r.Close())
		if enabled {
			next, err := inputcache.NewManager(cfg.Cache)
			require.NoError(t, err)
			require.NoError(t, next.Close())
		}
	}
}

func TestEngineResourcesCacheFailureCanRecover(t *testing.T) {
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "engine-test")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "engine-test-secret")
	setEngineStorageSecrets(t)
	cfg := DefaultEngineApplicationConfig()
	cfg.Storage.GatewayNodeID = "storage-test"
	cfg.Database.Path = filepath.Join(t.TempDir(), "runtime.db")
	cfg.Cache.Dir = filepath.Join(t.TempDir(), "cache")
	require.NoError(t, os.WriteFile(cfg.Cache.Dir, []byte("not a directory"), 0o600))
	r, err := OpenEngineResources(context.Background(), cfg)
	require.ErrorContains(t, err, "initialize engine input cache")
	require.Nil(t, r)
	require.NoError(t, os.Remove(cfg.Cache.Dir))
	r, err = OpenEngineResources(context.Background(), cfg)
	require.NoError(t, err)
	require.NoError(t, r.Close())
}
