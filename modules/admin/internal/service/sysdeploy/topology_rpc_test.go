package sysdeploy_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayauth/authtest"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/config"
	"github.com/mooyang-code/moox/modules/admin/internal/service/database"
	"github.com/mooyang-code/moox/modules/admin/internal/service/keys"
	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy"
	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy/rpc"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

func TestHostTopologyAPIOverRealTRPC(t *testing.T) {
	const master = "fixture-only-topology-gateway-master-0123456789"
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY", master)
	manager := database.NewManager()
	require.NoError(t, manager.Initialize(&config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "admin.db")}))
	db := manager.GetDB()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.Exec(schema.AdminSQL()).Error)
	require.False(t, db.Migrator().HasTable("t_service_deployments"))
	require.False(t, db.Migrator().HasTable("t_gateway_nodes"))
	methods := pb.File_sysdeploy_service_proto.Services().ByName("SysDeploy").Methods()
	require.Equal(t, 9, methods.Len())
	require.Nil(t, methods.ByName("ListServiceDeployments"))

	service := sysdeploy.NewService(manager, "control")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	svc := server.New(server.WithTransport(transport.NewServerTransport()), server.WithListener(listener), server.WithAddress(listener.Addr().String()), server.WithNetwork("tcp"), server.WithProtocol("trpc"))
	pb.RegisterSysDeployService(svc, rpc.NewService(service))
	go func() { _ = svc.Serve() }()
	t.Cleanup(func() { _ = svc.Close(nil) })
	proxy := pb.NewSysDeployClientProxy(client.WithTransport(transport.NewClientTransport()), client.WithTarget("ip://"+listener.Addr().String()), client.WithNetwork("tcp"), client.WithProtocol("trpc"), client.WithTimeout(3*time.Second))
	ctx := context.Background()
	check := func(ret *pb.RetInfo, err error) {
		t.Helper()
		require.NoError(t, err)
		require.NotNil(t, ret)
		require.Equal(t, pb.ErrorCode_SUCCESS, ret.GetCode(), ret.GetMsg())
	}
	hosts, err := proxy.ListHosts(ctx, &pb.ListDeploymentHostsReq{})
	check(hosts.GetRetInfo(), err)
	require.Empty(t, hosts.GetHosts(), "service construction must not seed host configuration")
	catalog, err := proxy.GetCatalog(ctx, &pb.GetCatalogReq{})
	check(catalog.GetRetInfo(), err)
	require.Equal(t, servicecatalog.EmbeddedYAML(), []byte(catalog.GetCatalogYaml()))
	require.Equal(t, "control", catalog.GetControlHostId())
	sum := sha256.Sum256([]byte(catalog.GetCatalogYaml()))
	require.Equal(t, hex.EncodeToString(sum[:]), catalog.GetSha256())
	_, err = servicecatalog.Decode(bytes.NewBufferString(catalog.GetCatalogYaml()))
	require.NoError(t, err)
	for _, spec := range []*pb.SyncHostDeploymentsReq{
		{HostId: "control", Address: "control.example.test", ComponentIds: []string{"admin"}},
		{HostId: "storage", Address: "storage.example.test", ComponentIds: []string{"storage-primary"}},
	} {
		response, err := proxy.SyncHostDeployments(ctx, spec)
		check(response.GetRetInfo(), err)
	}
	keyStore, err := keys.NewStore(db)
	require.NoError(t, err)
	publicKey := func(label string) string {
		return gatewayauth.EncodePublicKey(authtest.Key(label).Public().(ed25519.PublicKey))
	}
	require.NoError(t, keyStore.SetHostPublicKey(ctx, "storage", publicKey("host/storage")))
	hosts, err = proxy.ListHosts(ctx, &pb.ListDeploymentHostsReq{})
	check(hosts.GetRetInfo(), err)
	require.Len(t, hosts.GetHosts(), 2)
	page, err := proxy.ListHosts(ctx, &pb.ListDeploymentHostsReq{Page: &pb.Page{Size: 1}})
	check(page.GetRetInfo(), err)
	require.Len(t, page.GetHosts(), 1)
	require.True(t, page.GetPageResult().GetHasMore())
	require.EqualValues(t, 2, page.GetPageResult().GetTotal())
	page, err = proxy.ListHosts(ctx, &pb.ListDeploymentHostsReq{Page: &pb.Page{Page: 2, Size: 1}})
	check(page.GetRetInfo(), err)
	require.Equal(t, "storage", page.GetHosts()[0].GetHostId())
	require.False(t, page.GetPageResult().GetHasMore())
	deployments, err := proxy.ListDeployments(ctx, &pb.ListDeploymentsReq{HostId: "storage"})
	check(deployments.GetRetInfo(), err)
	require.Len(t, deployments.GetDeployments(), 3, "two host components are maintained automatically")
	directory, err := proxy.GetDirectory(ctx, &pb.GetDirectoryReq{})
	check(directory.GetRetInfo(), err)
	require.Contains(t, directory.GetDirectory().GetServices(), "trpc.moox.storage.Metadata")
	routes, err := proxy.GetHostRoutes(ctx, &pb.GetHostRoutesReq{HostId: "storage"})
	check(routes.GetRetInfo(), err)
	require.NotEmpty(t, routes.GetDefinitionHash())
	require.EqualValues(t, 1, routes.GetSnapshotSchemaVersion())
	compiledAt, err := time.Parse(time.RFC3339Nano, routes.GetCompiledAt())
	require.NoError(t, err)
	require.WithinDuration(t, time.Now(), compiledAt, 5*time.Second)
	require.NotEmpty(t, routes.GetRoutes())
	require.NotEmpty(t, routes.GetGatewayStatus().GetExpectedHash())
	require.NotEqual(t, routes.GetDefinitionHash(), routes.GetGatewayStatus().GetExpectedHash(), "definition hash must not stand in for the final snapshot hash, which covers the public keys")
	require.Empty(t, routes.GetGatewayStatus().GetLastSeenAt(), "reading desired state must not create a heartbeat")
	definitionHash, expectedHash := routes.GetDefinitionHash(), routes.GetGatewayStatus().GetExpectedHash()
	require.NoError(t, keyStore.SetHostPublicKey(ctx, "storage", publicKey("host/storage-rotated")))
	routes, err = proxy.GetHostRoutes(ctx, &pb.GetHostRoutesReq{HostId: "storage"})
	check(routes.GetRetInfo(), err)
	require.Equal(t, definitionHash, routes.GetDefinitionHash(), "key rotation does not change route definitions")
	require.NotEqual(t, expectedHash, routes.GetGatewayStatus().GetExpectedHash(), "desired hash must include key rotation before the next gateway heartbeat")
	require.Empty(t, routes.GetGatewayStatus().GetLastSeenAt())
	topology, err := sysdeploy.NewTopologyDAO(db, "control")
	require.NoError(t, err)
	_, snapshot, err := topology.CompileSnapshot(ctx, "storage")
	require.NoError(t, err)
	require.Equal(t, snapshot.GetHash(), routes.GetGatewayStatus().GetExpectedHash())
	routes, err = proxy.GetHostRoutes(ctx, &pb.GetHostRoutesReq{HostId: "unknown"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_NOT_FOUND, routes.GetRetInfo().GetCode())
	deployment, err := proxy.SetDeploymentStatus(ctx, &pb.SetDeploymentStatusReq{HostId: "storage", ComponentId: "storage-primary", Status: "disabled"})
	check(deployment.GetRetInfo(), err)
	directory, err = proxy.GetDirectory(ctx, &pb.GetDirectoryReq{})
	check(directory.GetRetInfo(), err)
	require.NotContains(t, directory.GetDirectory().GetServices(), "trpc.moox.storage.Metadata")
	host, err := proxy.SetHostStatus(ctx, &pb.SetHostStatusReq{HostId: "storage", Status: "disabled"})
	check(host.GetRetInfo(), err)
	directory, err = proxy.GetDirectory(ctx, &pb.GetDirectoryReq{})
	check(directory.GetRetInfo(), err)
	require.NotContains(t, directory.GetDirectory().GetHosts(), "storage")
	refused, err := proxy.DeleteHost(ctx, &pb.DeleteDeploymentHostReq{HostId: "storage"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, refused.GetRetInfo().GetCode(), "disabled business deployments still prevent deletion")
	synced, err := proxy.SyncHostDeployments(ctx, &pb.SyncHostDeploymentsReq{HostId: "storage", Address: "storage.example.test"})
	check(synced.GetRetInfo(), err)
	deleted, err := proxy.DeleteHost(ctx, &pb.DeleteDeploymentHostReq{HostId: "storage"})
	check(deleted.GetRetInfo(), err)
	refused, err = proxy.DeleteHost(ctx, &pb.DeleteDeploymentHostReq{HostId: "control"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, refused.GetRetInfo().GetCode())
}
