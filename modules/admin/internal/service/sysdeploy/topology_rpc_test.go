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
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/config"
	"github.com/mooyang-code/moox/modules/admin/internal/service/database"
	"github.com/mooyang-code/moox/modules/admin/internal/service/keys"
	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

func TestHostTopologyAPIOverRealTRPC(t *testing.T) {
	const master = "fixture-only-topology-gateway-master-0123456789"
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
	require.Equal(t, 5, methods.Len())
	require.Nil(t, methods.ByName("ListServiceDeployments"))
	require.Nil(t, methods.ByName("GetDirectory"), "目录不再由 Admin 展开下发")

	service, err := sysdeploy.NewService(manager, "control", master)
	require.NoError(t, err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	svc := server.New(server.WithTransport(transport.NewServerTransport()), server.WithListener(listener), server.WithAddress(listener.Addr().String()), server.WithNetwork("tcp"), server.WithProtocol("trpc"))
	pb.RegisterSysDeployService(svc, service)
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
	declare := func(storage *pb.SyncTopologyHost) *pb.SyncTopologyReq {
		hosts := []*pb.SyncTopologyHost{{HostId: "control", Enabled: true, Address: "control.example.test", ComponentIds: []string{"admin"}}}
		if storage != nil {
			hosts = append(hosts, storage)
		}
		return &pb.SyncTopologyReq{Hosts: hosts}
	}
	storage := &pb.SyncTopologyHost{HostId: "storage", Enabled: true, Address: "storage.example.test", ComponentIds: []string{"storage-primary"}}
	response, err := proxy.SyncTopology(ctx, declare(storage))
	check(response.GetRetInfo(), err)
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
	topology, err := sysdeploy.NewTopologyDAO(db, "control")
	require.NoError(t, err)
	// 路由与目录都按登记的部署和内嵌目录现算：routedServices 是一台主机路由里出现的服务。
	routedServices := func(routes *pb.GetHostRoutesRsp) map[string]bool {
		services := map[string]bool{}
		for _, route := range routes.GetRoutes() {
			services[route.GetServicePath()] = true
		}
		return services
	}
	compiled, err := topology.Compile(ctx, "control")
	require.NoError(t, err)
	require.Equal(t, []string{"storage"}, compiled.Directory.Services["trpc.moox.storage.Metadata"])
	routes, err := proxy.GetHostRoutes(ctx, &pb.GetHostRoutesReq{HostId: "storage"})
	check(routes.GetRetInfo(), err)
	require.Equal(t, "storage", routes.GetHostId())
	require.EqualValues(t, 2, routes.GetSnapshotSchemaVersion())
	require.Contains(t, routedServices(routes), "trpc.moox.storage.Metadata")
	require.NotContains(t, routedServices(routes), "trpc.moox.ops.SysDeploy", "路由只列本机部署的服务")
	for _, route := range routes.GetRoutes() {
		require.NotEmpty(t, route.GetCallers(), route.GetMethod())
		require.Positive(t, route.GetTimeoutMs())
		require.Positive(t, route.GetMaxBodyBytes())
		require.True(t, strings.HasPrefix(route.GetAddress(), "127.0.0.1:"), route.GetAddress())
	}
	require.WithinDuration(t, time.Now(), time.UnixMilli(routes.GetCompiledAt()), 5*time.Second)
	require.NotEmpty(t, routes.GetRoutes())
	require.NotEmpty(t, routes.GetGatewayStatus().GetExpectedHash())
	require.Empty(t, routes.GetGatewayStatus().GetLastSeenAt(), "reading desired state must not create a heartbeat")
	// 期望哈希就是快照哈希，全部主机相同。
	controlRoutes, err := proxy.GetHostRoutes(ctx, &pb.GetHostRoutesReq{HostId: "control"})
	check(controlRoutes.GetRetInfo(), err)
	require.Equal(t, routes.GetGatewayStatus().GetExpectedHash(), controlRoutes.GetGatewayStatus().GetExpectedHash())
	require.Contains(t, routedServices(controlRoutes), "trpc.moox.ops.SysDeploy")
	require.NotContains(t, routedServices(controlRoutes), "trpc.moox.storage.Metadata")
	previousRoutes, expectedHash := routes.GetRoutes(), routes.GetGatewayStatus().GetExpectedHash()
	require.NoError(t, keyStore.SetHostPublicKey(ctx, "storage", publicKey("host/storage-rotated")))
	routes, err = proxy.GetHostRoutes(ctx, &pb.GetHostRoutesReq{HostId: "storage"})
	check(routes.GetRetInfo(), err)
	require.Len(t, routes.GetRoutes(), len(previousRoutes))
	for i, route := range routes.GetRoutes() {
		require.True(t, proto.Equal(previousRoutes[i], route), "key rotation does not change route definitions")
	}
	require.NotEqual(t, expectedHash, routes.GetGatewayStatus().GetExpectedHash(), "desired hash must include key rotation before the next gateway heartbeat")
	require.Empty(t, routes.GetGatewayStatus().GetLastSeenAt())
	snapshot, err := topology.Snapshot(ctx)
	require.NoError(t, err)
	require.Equal(t, snapshot.GetHash(), routes.GetGatewayStatus().GetExpectedHash())
	require.Equal(t, snapshot.GetSchemaVersion(), routes.GetSnapshotSchemaVersion())
	routes, err = proxy.GetHostRoutes(ctx, &pb.GetHostRoutesReq{HostId: "unknown"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_NOT_FOUND, routes.GetRetInfo().GetCode())
	storage.DisabledComponentIds = []string{"storage-primary"}
	response, err = proxy.SyncTopology(ctx, declare(storage))
	check(response.GetRetInfo(), err)
	compiled, err = topology.Compile(ctx, "control")
	require.NoError(t, err)
	require.NotContains(t, compiled.Directory.Services, "trpc.moox.storage.Metadata")
	require.Contains(t, compiled.Directory.Hosts, "storage")
	routes, err = proxy.GetHostRoutes(ctx, &pb.GetHostRoutesReq{HostId: "storage"})
	check(routes.GetRetInfo(), err)
	require.NotContains(t, routedServices(routes), "trpc.moox.storage.Metadata", "停用的部署撤掉路由")
	require.NotEmpty(t, routes.GetRoutes(), "主机上其余组件的路由还在")
	require.NotEqual(t, snapshot.GetHash(), routes.GetGatewayStatus().GetExpectedHash(), "部署变了，期望哈希跟着变")
	storage.Enabled = false
	response, err = proxy.SyncTopology(ctx, declare(storage))
	check(response.GetRetInfo(), err)
	compiled, err = topology.Compile(ctx, "control")
	require.NoError(t, err)
	require.NotContains(t, compiled.Directory.Hosts, "storage")
	routes, err = proxy.GetHostRoutes(ctx, &pb.GetHostRoutesReq{HostId: "storage"})
	check(routes.GetRetInfo(), err)
	require.Empty(t, routes.GetRoutes(), "停用的主机没有路由")
	// 声明里没有的主机删除；控制机不能被删掉（拓扑要求它在且启用）。
	response, err = proxy.SyncTopology(ctx, declare(nil))
	check(response.GetRetInfo(), err)
	hosts, err = proxy.ListHosts(ctx, &pb.ListDeploymentHostsReq{})
	check(hosts.GetRetInfo(), err)
	require.Len(t, hosts.GetHosts(), 1)
	refused, err := proxy.SyncTopology(ctx, &pb.SyncTopologyReq{Hosts: []*pb.SyncTopologyHost{{HostId: "storage", Enabled: true, Address: "storage.example.test"}}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, refused.GetRetInfo().GetCode())
}
