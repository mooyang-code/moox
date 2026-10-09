package sysdeploy_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/config"
	"github.com/mooyang-code/moox/modules/admin/internal/service/database"
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
	manager := database.NewManager()
	require.NoError(t, manager.Initialize(&config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "admin.db")}))
	db := manager.GetDB()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.Exec(schema.AdminSQL()).Error)
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
	// During the staged migration, the legacy read path also traverses the real
	// adapter, service and SQLite database instead of a global function patch.
	legacyDAO := sysdeploy.NewDAO(db)
	require.NoError(t, legacyDAO.CreateGatewayNode(ctx, &sysdeploy.GatewayNode{NodeID: "legacy-control", Name: "legacy", PublicAddress: "https://legacy.example.test", Status: "enabled"}))
	legacyRow := sysdeploy.DefaultDeployments("legacy-control")[0]
	require.NoError(t, legacyDAO.Create(ctx, &legacyRow))
	legacy, err := proxy.ListServiceDeployments(ctx, &pb.ListServiceDeploymentsReq{ServiceName: legacyRow.ServiceName})
	check(legacy.GetRetInfo(), err)
	require.Len(t, legacy.GetDeployments(), 1)
	require.Equal(t, "legacy-control", legacy.GetDeployments()[0].GetNodeId())
	catalog, err := proxy.GetCatalog(ctx, &pb.GetCatalogReq{})
	check(catalog.GetRetInfo(), err)
	require.Equal(t, servicecatalog.EmbeddedYAML(), []byte(catalog.GetCatalogYaml()))
	sum := sha256.Sum256([]byte(catalog.GetCatalogYaml()))
	require.Equal(t, hex.EncodeToString(sum[:]), catalog.GetSha256())
	_, err = servicecatalog.Decode(bytes.NewBufferString(catalog.GetCatalogYaml()))
	require.NoError(t, err)
	for _, spec := range []*pb.SyncHostPlacementsReq{
		{HostId: "control", Address: "control.example.test", ComponentIds: []string{"admin", "console-proxy", "web-host"}},
		{HostId: "storage", Address: "storage.example.test", ComponentIds: []string{"storage-primary"}},
	} {
		response, err := proxy.SyncHostPlacements(ctx, spec)
		check(response.GetRetInfo(), err)
	}
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
	placements, err := proxy.ListPlacements(ctx, &pb.ListPlacementsReq{HostId: "storage"})
	check(placements.GetRetInfo(), err)
	require.Len(t, placements.GetPlacements(), 3, "two host components are maintained automatically")
	directory, err := proxy.GetDirectory(ctx, &pb.GetDirectoryReq{})
	check(directory.GetRetInfo(), err)
	require.Contains(t, directory.GetDirectory().GetServices(), "trpc.moox.storage.Metadata")
	routes, err := proxy.GetHostRoutes(ctx, &pb.GetHostRoutesReq{HostId: "storage"})
	check(routes.GetRetInfo(), err)
	require.NotEmpty(t, routes.GetDefinitionHash())
	require.NotEmpty(t, routes.GetRoutes())
	require.Empty(t, routes.GetGatewayStatus().GetExpectedHash(), "definition hash must not stand in for the final signed-key snapshot hash")
	routes, err = proxy.GetHostRoutes(ctx, &pb.GetHostRoutesReq{HostId: "unknown"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_NOT_FOUND, routes.GetRetInfo().GetCode())
	placement, err := proxy.SetPlacementStatus(ctx, &pb.SetPlacementStatusReq{HostId: "storage", ComponentId: "storage-primary", Status: "disabled"})
	check(placement.GetRetInfo(), err)
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
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, refused.GetRetInfo().GetCode(), "disabled business placements still prevent deletion")
	synced, err := proxy.SyncHostPlacements(ctx, &pb.SyncHostPlacementsReq{HostId: "storage", Address: "storage.example.test"})
	check(synced.GetRetInfo(), err)
	deleted, err := proxy.DeleteHost(ctx, &pb.DeleteDeploymentHostReq{HostId: "storage"})
	check(deleted.GetRetInfo(), err)
	refused, err = proxy.DeleteHost(ctx, &pb.DeleteDeploymentHostReq{HostId: "control"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, refused.GetRetInfo().GetCode())
}
