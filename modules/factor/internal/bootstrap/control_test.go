package bootstrap

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/catalogsync"
	"github.com/mooyang-code/moox/modules/factor/internal/health"
	"github.com/mooyang-code/moox/packages/jetstream/testkit"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/server"
)

func TestControlRuntimeCloseOrderAndFailureCleanup(t *testing.T) {
	var calls []string
	failure := errors.New("catalog close failed")
	r := &ControlRuntime{Health: health.New("factor", "test", "", ""),
		cancel:         func() { calls = append(calls, "cancel") },
		waitReconcile:  func() { calls = append(calls, "reconcile") },
		stopCatalog:    func() error { calls = append(calls, "catalog"); return failure },
		closeResources: func() error { calls = append(calls, "resources"); return nil },
	}
	r.Health.SetReady(true)
	for range 2 {
		if !errors.Is(r.Close(), failure) {
			t.Fatal("lost close error")
		}
	}
	if !reflect.DeepEqual(calls, []string{"cancel", "reconcile", "catalog", "resources"}) {
		t.Fatalf("close order: %v", calls)
	}
	if r.Health.Ready() {
		t.Fatal("closed runtime ready")
	}
}

func TestValidateControlRuntimeRejectsEngineServices(t *testing.T) {
	if err := validateControlRuntime(nil, nil); err == nil {
		t.Fatal("accepted nil config")
	}
	s := &server.Server{}
	cfg := DefaultControlConfig()
	if err := validateControlRuntime(s, cfg); err == nil {
		t.Fatal("accepted missing FactorMgr")
	}
	s.AddService(controlHealthService, &engineRegisteredService{})
	if err := validateControlRuntime(s, cfg); err == nil {
		t.Fatal("accepted missing FactorMgr")
	}
	s.AddService("trpc.moox.factor.FactorMgr", &engineRegisteredService{})
	s.AddService(engineHealthService, &engineRegisteredService{})
	if err := validateControlRuntime(s, cfg); err == nil {
		t.Fatal("accepted engine health service")
	}
}

func TestInitializeControlServesCatalogWithoutPython(t *testing.T) {
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "control-test")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "control-test-secret")
	t.Setenv("MOOX_HEALTH_AUTH_ACCESS_KEY", "control-health-test")
	t.Setenv("MOOX_HEALTH_AUTH_SECRET_KEY", "control-health-test-secret")
	t.Setenv("MOOX_FACTOR_ENGINE_PYTHON_BIN", filepath.Join(t.TempDir(), "missing-python"))
	bus := testkit.Start(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s := &server.Server{}
	healthService, mgr := &engineRegisteredService{}, &engineRegisteredService{}
	s.AddService(controlHealthService, healthService)
	s.AddService("trpc.moox.factor.FactorMgr", mgr)
	cfg := DefaultControlConfig()
	cfg.Storage.GatewayNodeID = "storage-test"
	cfg.Database.Path = filepath.Join(t.TempDir(), "catalog.db")
	cfg.ArtifactsDir = t.TempDir()
	cfg.EventBus.URLs = []string{bus.URL()}
	runtime, err := InitializeControl(ctx, s, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })
	require.Equal(t, 1, healthService.registrations)
	require.Equal(t, 1, mgr.registrations)
	snapshot, err := runtime.Resources.Store.CatalogSnapshot(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, snapshot.Revision)
	nc, err := nats.Connect(bus.URL())
	require.NoError(t, err)
	defer nc.Close()
	got, err := catalogsync.FetchSnapshot(ctx, nc)
	require.NoError(t, err)
	require.EqualValues(t, 1, got.Revision)
	require.True(t, runtime.snapshot(ctx).Ready)
	require.Equal(t, 0, runtime.snapshot(ctx).Details["python_workers"])
	require.NoError(t, runtime.Close())
	require.False(t, runtime.snapshot(ctx).Ready)
}
