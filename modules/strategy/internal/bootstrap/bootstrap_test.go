package bootstrap

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	_ "trpc.group/trpc-go/trpc-filter/recovery"
	_ "trpc.group/trpc-go/trpc-filter/validation"
	trpc "trpc.group/trpc-go/trpc-go"
	_ "trpc.group/trpc-go/trpc-metrics-prometheus"
)

func TestInitializeStartsReadyConsumerAndClosesOwnedResources(t *testing.T) {
	for _, name := range []string{"MOOX_EVENTBUS_NATS_URL", "MOOX_EVENTBUS_URL", "NATS_URL"} {
		t.Setenv(name, "")
	}
	t.Setenv("MOOX_INSTANCE_ID", "strategy-test")
	t.Setenv("MOOX_NODE_ID", "strategy-node")
	t.Setenv("MOOX_BOOT_ID", "strategy-boot")
	t.Setenv("MOOX_HEALTH_AUTH_ACCESS_KEY", "test-access")
	t.Setenv("MOOX_HEALTH_AUTH_SECRET_KEY", "test-secret")
	t.Setenv("MOOX_HEALTH_AUTH_VERSION", "test-v1")
	ns, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir()})
	require.NoError(t, err)
	ns.Start()
	require.True(t, ns.ReadyForConnections(10*time.Second))
	t.Cleanup(func() { ns.Shutdown(); ns.WaitForShutdown() })
	nc, err := nats.Connect(ns.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	filter, err := registry.FamilyPattern(events.ViewDataReady)
	require.NoError(t, err)
	_, err = js.AddStream(&nats.StreamConfig{Name: "MOOX_STORAGE", Subjects: []string{filter}, Storage: nats.MemoryStorage})
	require.NoError(t, err)
	cfg, _ := strategyGatewayFixture(t, ns.ClientURL())
	trpcConfig, err := trpc.LoadConfig(filepath.Join("..", "..", "config", "trpc_go.yaml"))
	require.NoError(t, err)
	srv, closeFn, err := Initialize(t.Context(), trpc.NewServerWithConfig(trpcConfig), cfg)
	require.NoError(t, err)
	require.NotNil(t, srv)
	require.NotNil(t, closeFn)
	t.Cleanup(func() { require.NoError(t, closeFn()) })
	consumer, err := js.ConsumerInfo("MOOX_STORAGE", cfg.EventBus.ConsumerName)
	require.NoError(t, err)
	require.Equal(t, filter, consumer.Config.FilterSubject)
	require.NoError(t, closeFn())
	require.NoError(t, closeFn())
}

func TestInitializeRejectsMissingDeploymentIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.yaml")
	raw := "database: " + filepath.Join(t.TempDir(), "strategy.db") + "\nstorage:\n  app_key: fixture-primary-role-key\n  view_app_key: fixture-view-role-key\n"
	require.NoError(t, os.WriteFile(path, []byte(raw), 0o600))
	cfg, err := Load(path)
	require.NoError(t, err)
	srv, closeFn, err := Initialize(t.Context(), nil, cfg)
	require.ErrorContains(t, err, "requires key_id")
	require.Nil(t, srv)
	require.Nil(t, closeFn)
}
