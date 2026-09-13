package bootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/catalogsync"
	"github.com/mooyang-code/moox/modules/factor/internal/health"
	"github.com/mooyang-code/moox/modules/factor/internal/merge"
	"github.com/mooyang-code/moox/packages/jetstream/testkit"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/server"
)

func TestRoleBoundaryControlStartsWithoutPython(t *testing.T) {
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "role-boundary")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "role-boundary-secret")
	t.Setenv("MOOX_HEALTH_AUTH_ACCESS_KEY", "role-health")
	t.Setenv("MOOX_HEALTH_AUTH_SECRET_KEY", "role-health-secret")
	t.Setenv("MOOX_FACTOR_ENGINE_PYTHON_BIN", filepath.Join(t.TempDir(), "missing-python"))
	bus := testkit.Start(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s := &server.Server{}
	s.AddService(controlHealthService, &engineRegisteredService{})
	s.AddService("trpc.moox.factor.FactorMgr", &engineRegisteredService{})
	cfg := DefaultControlConfig()
	cfg.Storage.GatewayNodeID = "storage-test"
	cfg.Database.Path = filepath.Join(t.TempDir(), "catalog.db")
	cfg.ArtifactsDir = t.TempDir()
	cfg.EventBus.URLs = []string{bus.URL()}
	runtime, err := InitializeControl(ctx, s, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = runtime.Close() })
	require.Equal(t, 0, runtime.snapshot(ctx).Details["python_workers"])
}

func TestRoleBoundaryRejectsMissingGatewayNode(t *testing.T) {
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "role-boundary")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "role-boundary-secret")
	cfg := DefaultControlConfig()
	cfg.Database.Path = filepath.Join(t.TempDir(), "catalog.db")
	cfg.ArtifactsDir = t.TempDir()
	cfg.Storage.GatewayNodeID = ""
	_, err := OpenControlResources(context.Background(), cfg)
	require.ErrorContains(t, err, "gateway target node")
	engine := DefaultEngineApplicationConfig()
	engine.Database.Path = filepath.Join(t.TempDir(), "runtime.db")
	engine.Storage.GatewayNodeID = ""
	_, err = OpenEngineResources(context.Background(), engine)
	require.ErrorContains(t, err, "gateway target node")
}

func TestRoleBoundaryKeepsIndependentDatabases(t *testing.T) {
	control := DefaultControlConfig()
	engine := DefaultEngineApplicationConfig()
	require.NotEqual(t, control.Database.Path, engine.Database.Path)
	cfg, err := merge.LoadProcessConfig(filepath.Join("..", "..", "config", "merge-app.yaml"))
	require.NoError(t, err)
	require.NotEqual(t, control.Database.Path, cfg.Database.Path)
	require.NotEqual(t, engine.Database.Path, cfg.Database.Path)
}

func TestRoleBoundaryControlRestartDoesNotTouchEngineStore(t *testing.T) {
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "role-boundary")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "role-boundary-secret")
	t.Setenv("MOOX_HEALTH_AUTH_ACCESS_KEY", "role-health")
	t.Setenv("MOOX_HEALTH_AUTH_SECRET_KEY", "role-health-secret")
	bus := testkit.Start(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	enginePath := filepath.Join(t.TempDir(), "engine-runtime.db")
	require.NoError(t, os.WriteFile(enginePath, []byte("engine-owned"), 0o600))
	s := &server.Server{}
	s.AddService(controlHealthService, &engineRegisteredService{})
	s.AddService("trpc.moox.factor.FactorMgr", &engineRegisteredService{})
	cfg := DefaultControlConfig()
	cfg.Storage.GatewayNodeID = "storage-test"
	cfg.Database.Path = filepath.Join(t.TempDir(), "catalog.db")
	cfg.ArtifactsDir = t.TempDir()
	cfg.EventBus.URLs = []string{bus.URL()}
	runtime, err := InitializeControl(ctx, s, cfg)
	require.NoError(t, err)
	require.NoError(t, runtime.Close())
	raw, err := os.ReadFile(enginePath)
	require.NoError(t, err)
	require.Equal(t, []byte("engine-owned"), raw)
}

func TestRoleBoundaryMissedCatalogNotifyRecoversFromSnapshot(t *testing.T) {
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "role-boundary")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "role-boundary-secret")
	t.Setenv("MOOX_HEALTH_AUTH_ACCESS_KEY", "role-health")
	t.Setenv("MOOX_HEALTH_AUTH_SECRET_KEY", "role-health-secret")
	bus := testkit.Start(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	catalogPath := filepath.Join(t.TempDir(), "catalog.db")
	artifacts := t.TempDir()
	start := func() *ControlRuntime {
		s := &server.Server{}
		s.AddService(controlHealthService, &engineRegisteredService{})
		s.AddService("trpc.moox.factor.FactorMgr", &engineRegisteredService{})
		cfg := DefaultControlConfig()
		cfg.Storage.GatewayNodeID = "storage-test"
		cfg.Database.Path = catalogPath
		cfg.ArtifactsDir = artifacts
		cfg.EventBus.URLs = []string{bus.URL()}
		runtime, err := InitializeControl(ctx, s, cfg)
		require.NoError(t, err)
		return runtime
	}
	first := start()
	nc, err := nats.Connect(bus.URL())
	require.NoError(t, err)
	defer nc.Close()
	got, err := catalogsync.FetchSnapshot(ctx, nc)
	require.NoError(t, err)
	require.EqualValues(t, 1, got.Revision)
	require.NoError(t, first.Close())
	second := start()
	t.Cleanup(func() { _ = second.Close() })
	got, err = catalogsync.FetchSnapshot(ctx, nc)
	require.NoError(t, err)
	require.EqualValues(t, 1, got.Revision)
}

func TestRoleBoundaryCloseDrainsBeforeClosingStore(t *testing.T) {
	var calls []string
	r := &EngineRuntime{
		Health: health.New("factor-engine", "test", "", ""),
		cancel: func() { calls = append(calls, "cancel") },
		stopSubject: func() error {
			calls = append(calls, "subject")
			return nil
		},
		stopCatalog: func() error {
			calls = append(calls, "catalog")
			return nil
		},
		closeResources: func() error {
			calls = append(calls, "resources")
			return errors.New("store still in use")
		},
	}
	r.Health.SetReady(true)
	err := r.Close()
	require.ErrorContains(t, err, "store still in use")
	require.Equal(t, []string{"cancel", "subject", "catalog", "resources"}, calls)
	require.False(t, r.Health.Ready())
}
