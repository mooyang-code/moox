package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/config"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/health"
	cloudnoderpc "github.com/mooyang-code/moox/modules/cloudnode/internal/rpc"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	"github.com/mooyang-code/moox/modules/cloudnode/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/server"
)

func TestStartNodeBatchRunnerUsesRuntimeContextAndRecoversInterruptedItems(t *testing.T) {
	dbm, err := store.Open(&config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "cloudnode.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = dbm.Close() })
	require.NoError(t, dbm.ApplySchema(schema.AllSQL()))
	catalog := dbm.Catalog()
	require.NoError(t, catalog.CreateNodeBatch(context.Background(), store.NodeBatchCreate{
		SpaceID: "crypto", JobID: "bootstrap-recovery", Operation: "create_nodes",
		Items: []store.NodeBatchItemCreate{{ItemID: "item-0", NodeID: "node-0", RequestJSON: `{}`}},
	}))
	taken, err := catalog.TakePendingNodeBatchItems(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, taken, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.NoError(t, startNodeBatchRunner(ctx, cloudnoderpc.New(dbm), config.Default()))

	aggregate, err := catalog.GetNodeBatch(context.Background(), "crypto", "bootstrap-recovery")
	require.NoError(t, err)
	assert.Equal(t, 1, aggregate.PendingCount)
	assert.Equal(t, store.NodeBatchPending, aggregate.Job.Status)
}

func TestCloudNodeHealthSnapshot(t *testing.T) {
	cfg := config.Default()
	dbm, err := store.Open(&config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "cloudnode.db")})
	if err != nil {
		t.Fatalf("initialize database: %v", err)
	}
	t.Cleanup(func() { _ = dbm.Close() })
	state := health.New("cloudnode", "cloudnode", "", "")
	rsp := cloudnodeHealthSnapshot(cfg, dbm, state)(context.Background())

	if rsp.Module != "cloudnode" || !rsp.Ready || rsp.Status != "ok" {
		t.Fatalf("health response = %+v", rsp)
	}
	if rsp.Details["queue_backend"] != "jetstream" {
		t.Fatalf("queue_backend = %v", rsp.Details["queue_backend"])
	}
}

func TestSCFHeartbeatTargetsUseEnvironmentAndLocalFallbacks(t *testing.T) {
	t.Setenv("MOOX_SCF_SERVICE_GATEWAY_TARGET", "")
	t.Setenv("MOOX_SCF_STORAGE_RPC_GATEWAY_TARGET", "")
	targets := scfHeartbeatTargetsFromEnv()
	assert.Equal(t, "http://127.0.0.1:11002", targets.ServiceGatewayTarget)
	assert.Equal(t, "ip://127.0.0.1:11003", targets.StorageRPCGatewayTarget)

	t.Setenv("MOOX_SCF_SERVICE_GATEWAY_TARGET", " https://gateway.example.com/service ")
	t.Setenv("MOOX_SCF_STORAGE_RPC_GATEWAY_TARGET", " ip://gateway.example.com:11003 ")
	targets = scfHeartbeatTargetsFromEnv()
	assert.Equal(t, "https://gateway.example.com/service", targets.ServiceGatewayTarget)
	assert.Equal(t, "ip://gateway.example.com:11003", targets.StorageRPCGatewayTarget)
}

func TestSCFHeartbeatMaintainerTimerConfig(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "config", "trpc_go.yaml"))
	require.NoError(t, err)
	text := string(data)
	for _, want := range []string{
		"name: " + scfHeartbeatMaintainerTimerService,
		"port: 11414",
		"network: \"*/9 * * * * *?startAtOnce=1\"",
		"protocol: timer",
		"timeout: 8000",
	} {
		assert.Truef(t, strings.Contains(text, want), "timer config missing %q", want)
	}
}

func TestSCFHeartbeatMaintainerTimerRegistersStartAtOnceHandler(t *testing.T) {
	s := newImmediateSCFHeartbeatTimerServer()
	started := make(chan struct{})
	require.NoError(t, registerSCFHeartbeatMaintainerHandler(s, func(context.Context) error {
		close(started)
		return nil
	}))
	serveDone := make(chan error, 1)
	go func() { serveDone <- s.Serve() }()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("startAtOnce heartbeat maintainer did not run")
	}
	select {
	case err := <-serveDone:
		t.Fatalf("timer server returned before shutdown: %v", err)
	default:
	}
	require.NoError(t, s.Close(nil))
	require.NoError(t, <-serveDone)
}

func newImmediateSCFHeartbeatTimerServer() *server.Server {
	cfg := &trpc.Config{}
	cfg.Server.Service = []*trpc.ServiceConfig{{
		Name: scfHeartbeatMaintainerTimerService, IP: "127.0.0.1", Port: 11414,
		Network: "*/9 * * * * *?startAtOnce=1", Protocol: "timer", Timeout: 8000,
	}}
	return trpc.NewServerWithConfig(cfg)
}
