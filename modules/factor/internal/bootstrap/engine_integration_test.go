package bootstrap

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/catalogsync"
	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/packages/jetstream/testkit"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/server"
)

type engineRegisteredService struct{ registrations int }

func (*engineRegisteredService) ServiceName() string       { return "test" }
func (s *engineRegisteredService) Register(any, any) error { s.registrations++; return nil }
func (*engineRegisteredService) Serve() error              { return nil }
func (*engineRegisteredService) Close(chan struct{}) error { return nil }

type engineStartupCatalog struct{}

func (engineStartupCatalog) CatalogSnapshot(context.Context) (*domain.CatalogSnapshot, error) {
	return &domain.CatalogSnapshot{Revision: 1}, nil
}

// Real NATS, replica SQLite and Python; tRPC registration is captured without
// listening on fixed ports. This verifies startup wiring, not factor writeback.
func TestInitializeEngineConnectsCatalogPythonAndConsumer(t *testing.T) {
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "engine-test")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "engine-test-secret")
	t.Setenv("MOOX_HEALTH_AUTH_ACCESS_KEY", "engine-health-test")
	t.Setenv("MOOX_HEALTH_AUTH_SECRET_KEY", "engine-health-test-secret")
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "primary-test")
	t.Setenv("MOOX_STORAGE_VIEW_AUTH_SECRET", "view-test")
	bus := testkit.Start(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	nc, err := nats.Connect(bus.URL())
	require.NoError(t, err)
	defer nc.Close()
	js, err := nc.JetStream()
	require.NoError(t, err)
	_, err = js.AddStream(&nats.StreamConfig{Name: "MOOX_STORAGE", Subjects: []string{"moox.event.storage.>"}, Storage: nats.MemoryStorage})
	require.NoError(t, err)
	_, err = catalogsync.ServeSnapshots(ctx, nc, engineStartupCatalog{})
	require.NoError(t, err)
	s := &server.Server{}
	healthService, catalogService := &engineRegisteredService{}, &engineRegisteredService{}
	s.AddService(engineHealthService, healthService)
	s.AddService(catalogTimerService, catalogService)
	cfg := DefaultEngineApplicationConfig()
	cfg.Cache.Enabled = false
	cfg.Storage.GatewayNodeID = "storage-test"
	cfg.Database.Path = filepath.Join(t.TempDir(), "runtime.db")
	cfg.EventBus.URLs = []string{bus.URL()}
	cfg.Engine.PythonWorkers = 1
	cfg.Engine.PythonBin = "python3"
	cfg.Engine.WorkerPath = filepath.Join("..", "..", "pyworker", "worker.py")
	cfg.Engine.FactorsDir = t.TempDir()
	runtime, err := InitializeEngine(ctx, s, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })
	require.Equal(t, 1, healthService.registrations)
	require.Equal(t, 1, catalogService.registrations)
	snapshot, err := runtime.Resources.Store.CatalogSnapshot(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, snapshot.Revision)
	require.True(t, runtime.Resources.PythonPool.Status().Ready)
	require.True(t, runtime.snapshot(ctx).Ready)
	require.NoError(t, runtime.Close())
	require.False(t, runtime.snapshot(ctx).Ready)
	require.False(t, runtime.Resources.PythonPool.Status().Ready)
	_, err = runtime.Resources.Store.CatalogSnapshot(ctx)
	require.Error(t, err)
}
