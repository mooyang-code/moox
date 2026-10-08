package rpc

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/admin/internal/service/gatewaycontrol"
	"github.com/mooyang-code/moox/modules/admin/internal/service/keys"
	"github.com/mooyang-code/moox/modules/admin/internal/service/placement"
	secretdao "github.com/mooyang-code/moox/modules/admin/internal/service/secret/dao"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newPlacementRPC(t *testing.T) *Service {
	t.Helper()
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY", "test-encryption-key-0123456789abcdef")
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "admin.db")+"?_pragma=foreign_keys(1)"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(schema.AdminSQL()).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	placements := placement.NewService(db, nil)
	return NewService(nil, placements, gatewaycontrol.NewService(placements, keys.NewService(secretdao.NewSecretDAO(db))))
}

func TestSysDeployV2Flow(t *testing.T) {
	svc := newPlacementRPC(t)
	ctx := context.Background()

	catalog, err := svc.GetCatalog(ctx, &pb.GetCatalogReq{})
	require.NoError(t, err)
	require.NotEmpty(t, catalog.GetChecksum())
	found := false
	for _, component := range catalog.GetComponents() {
		if component.GetId() == "cloudnode" {
			found = true
			for _, service := range component.GetServices() {
				for _, method := range service.GetMethods() {
					if method.GetName() == "CollectGarbage" {
						require.Equal(t, []string{"admin"}, method.GetCallers())
					}
				}
			}
		}
	}
	require.True(t, found)

	sync, err := svc.SyncHostPlacements(ctx, &pb.SyncHostPlacementsReq{
		Host:       &pb.DeployHostSpec{HostId: "control", Address: "106.53.107.122"},
		Components: []string{"console-proxy", "web-host", "admin", "monitor", "collector"},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, sync.GetRetInfo().GetCode(), sync.GetRetInfo().GetMsg())
	require.True(t, sync.GetHostCreated())

	bad, err := svc.SyncHostPlacements(ctx, &pb.SyncHostPlacementsReq{
		Host: &pb.DeployHostSpec{HostId: "control", Address: "106.53.107.122"}, Components: []string{"monitor"},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, bad.GetRetInfo().GetCode(), "移除受保护组件整体拒绝")

	placements, err := svc.ListPlacements(ctx, &pb.ListPlacementsReq{HostId: "control"})
	require.NoError(t, err)
	byID := map[string]*pb.DeployPlacement{}
	for _, item := range placements.GetPlacements() {
		byID[item.GetComponentId()] = item
	}
	require.True(t, byID["admin"].GetProtected())
	require.True(t, byID["host-gateway"].GetHostComponent())
	require.False(t, byID["monitor"].GetProtected())

	protected, err := svc.SetPlacementStatus(ctx, &pb.SetPlacementStatusReq{HostId: "control", ComponentId: "admin", Status: "disabled"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, protected.GetRetInfo().GetCode())
	host, err := svc.SetHostStatus(ctx, &pb.SetHostStatusReq{HostId: "control", Status: "disabled"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, host.GetRetInfo().GetCode())

	disabled, err := svc.SetPlacementStatus(ctx, &pb.SetPlacementStatusReq{HostId: "control", ComponentId: "collector", Status: "disabled"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, disabled.GetRetInfo().GetCode())
	require.Equal(t, "disabled", disabled.GetPlacement().GetStatus())

	routes, err := svc.GetHostRoutes(ctx, &pb.GetHostRoutesReq{HostId: "control"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, routes.GetRetInfo().GetCode(), routes.GetRetInfo().GetMsg())
	require.NotEmpty(t, routes.GetExpectedHash())
	require.Equal(t, "never_reported", routes.GetGateway().GetState())
	for _, route := range routes.GetRoutes() {
		require.NotEqual(t, "trpc.moox.collector.CollectMgr", route.GetServicePath(), "停用的部署没有路由")
	}

	directory, err := svc.GetDirectory(ctx, &pb.GetDirectoryReq{})
	require.NoError(t, err)
	require.NotEmpty(t, directory.GetDirectory().GetVersion())

	hosts, err := svc.ListHosts(ctx, &pb.ListDeployHostsReq{})
	require.NoError(t, err)
	require.Len(t, hosts.GetHosts(), 1)
	require.True(t, hosts.GetHosts()[0].GetProtected())

	missing, err := svc.GetHostRoutes(ctx, &pb.GetHostRoutesReq{HostId: "nowhere"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_NOT_FOUND, missing.GetRetInfo().GetCode())

	deleted, err := svc.DeleteHost(ctx, &pb.DeleteDeployHostReq{HostId: "control"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, deleted.GetRetInfo().GetCode())
}
