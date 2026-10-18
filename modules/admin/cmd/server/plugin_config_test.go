package main

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/glebarez/sqlite"
	authdao "github.com/mooyang-code/moox/modules/admin/internal/service/auth/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/gatewaycontrol"
	"github.com/mooyang-code/moox/modules/admin/internal/service/keys"
	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/plugin"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

func TestPluginConfigInitializes(t *testing.T) {
	t.Setenv("MOOX_OTEL_ENDPOINT", "")
	cfg, err := trpc.LoadConfig("../../config/trpc_go.yaml")
	if err != nil {
		t.Fatalf("load tRPC config: %v", err)
	}
	server := trpc.NewServerWithConfig(cfg, gatewaycontrol.ServerOption())
	if server == nil {
		t.Fatal("expected initialized tRPC server")
	}
}

func TestConfiguredGatewayControlFiltersPreserveSignedWireBytes(t *testing.T) {
	const master = "fixture-only-configured-control-master-0123456789"
	dir := t.TempDir()
	db, err := gorm.Open(sqlite.Open(filepath.Join(dir, "admin.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.Exec(schema.AdminSQL()).Error)
	topology, err := sysdeploy.NewTopologyDAO(db, "control")
	require.NoError(t, err)
	require.NoError(t, topology.SyncHostPlacements(context.Background(), sysdeploy.HostSpec{HostID: "control", Address: "control.example.test", Components: []string{"admin", "console-proxy", "web-host"}}))
	keyStore, err := keys.NewStore(db, master)
	require.NoError(t, err)
	_, err = keyStore.EnsureAll(context.Background())
	require.NoError(t, err)
	key, err := keyStore.Current(context.Background(), "host-gateway@control")
	require.NoError(t, err)
	badgerDB, err := badger.Open(badger.DefaultOptions(filepath.Join(dir, "cache")).WithLogger(nil))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, badgerDB.Close()) })
	cache, err := authdao.NewCacheDBFromBadger(badgerDB)
	require.NoError(t, err)
	implementation, err := gatewaycontrol.NewService(db, "control", master, authdao.NewUserDAO(db, cache))
	require.NoError(t, err)
	cfg, err := trpc.LoadConfig("../../config/trpc_go.yaml")
	require.NoError(t, err)
	// NewServerWithConfig deliberately leaves plugin startup to its caller.
	// Initialize the real metrics plugin, as NewServer does, on a test port.
	var metricsNode yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte("ip: 127.0.0.1\nport: 0\npath: /metrics\nenablepush: false\n"), &metricsNode))
	closePlugins, err := trpc.SetupPlugins(plugin.Config{"metrics": {"prometheus": metricsNode}})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, closePlugins()) })
	for _, service := range cfg.Server.Service {
		if service.Name == servicecatalog.GatewayControlPath {
			cfg.Server.Service = []*trpc.ServiceConfig{service}
			break
		}
	}
	require.Len(t, cfg.Server.Service, 1)
	cfg.Server.Admin.Port = 0
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	configured := trpc.NewServerWithConfig(cfg, gatewaycontrol.ServerOption(), server.WithListener(listener))
	svc := configured.Service(servicecatalog.GatewayControlPath)
	require.NoError(t, gatewaycontrol.Register(svc, implementation))
	go func() { _ = svc.Serve() }()
	t.Cleanup(func() { _ = svc.Close(nil) })
	proxy := pb.NewGatewayControlClientProxy(client.WithTransport(transport.NewClientTransport()), client.WithTarget("ip://"+listener.Addr().String()), client.WithNetwork("tcp"), client.WithProtocol("trpc"), client.WithTimeout(3*time.Second), client.WithCurrentSerializationType(codec.SerializationTypeNoop), client.WithFilter(gatewayauth.NewTRPCClientFilter(key.Credentials(), "control", nil)))
	response, err := proxy.PullSnapshot(context.Background(), &pb.PullSnapshotReq{HostId: "control"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, response.GetRetInfo().GetCode())
	require.True(t, response.Changed)
	hash, err := pb.SnapshotHash(response.Snapshot)
	require.NoError(t, err)
	require.Equal(t, hash, response.Snapshot.Hash, "configured filters must preserve snapshot key material")
}
