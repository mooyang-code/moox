package sysdeploy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/database"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newTestService(db *gorm.DB) *ServiceImpl {
	svc := NewService(&database.Manager{})
	svc.dao = NewDAO(db)
	return svc
}

func TestCompileGatewaySnapshot_ExcludesDisabledDeployment(t *testing.T) {
	dao := NewDAO(setupEmptySysDeployTestDB(t))
	ctx := context.Background()
	require.NoError(t, dao.CreateGatewayNode(ctx, &GatewayNode{NodeID: "node-a", Name: "A", PublicAddress: "https://a.example", Status: "enabled"}))
	for _, row := range []Deployment{
		{NodeID: "node-a", ServiceName: "active", Protocol: "http", Host: "127.0.0.1", Port: 1001, GatewayPath: "trpc.test.Active", GatewayServiceID: "active", GatewayEnabled: true, Status: "active"},
		{NodeID: "node-a", ServiceName: "disabled", Protocol: "http", Host: "127.0.0.1", Port: 1002, GatewayPath: "trpc.test.Disabled", GatewayServiceID: "disabled", GatewayEnabled: true, Status: "disabled"},
	} {
		require.NoError(t, dao.Create(ctx, &row))
	}
	snapshot, err := dao.CompileGatewaySnapshot(ctx, "node-a")
	require.NoError(t, err)
	require.Len(t, snapshot.Routes, 1)
	assert.Equal(t, "active", snapshot.Routes[0].ServiceID)
}

func TestServiceDeploymentCompositeRPCIsolationAndLists(t *testing.T) {
	db := setupEmptySysDeployTestDB(t)
	svc := newTestService(db)
	ctx := context.Background()
	for _, id := range []string{"node-a", "node-b"} {
		require.NoError(t, svc.dao.CreateGatewayNode(ctx, &GatewayNode{NodeID: id, Name: id, PublicAddress: "https://" + id + ".example", Status: "enabled"}))
	}
	for _, id := range []string{"node-a", "node-b"} {
		rsp, err := svc.CreateServiceDeployment(ctx, &pb.CreateServiceDeploymentReq{Deployment: &pb.ServiceDeployment{NodeId: id, ServiceName: "monitor", Protocol: "http", Host: "127.0.0.1", Port: 1100, Status: "active"}})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	}
	updated, err := svc.UpdateServiceDeployment(ctx, &pb.UpdateServiceDeploymentReq{NodeId: "node-a", ServiceName: "monitor", Deployment: &pb.ServiceDeployment{NodeId: "node-a", ServiceName: "monitor", Protocol: "http", Host: "127.0.0.1", Port: 2200, Status: "active"}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, updated.GetRetInfo().GetCode())
	other, err := svc.GetServiceDeployment(ctx, &pb.GetServiceDeploymentReq{NodeId: "node-b", ServiceName: "monitor"})
	require.NoError(t, err)
	assert.Equal(t, int32(1100), other.GetDeployment().GetPort())
	all, err := svc.ListActiveServiceDeployments(ctx, &pb.ListActiveServiceDeploymentsReq{})
	require.NoError(t, err)
	assert.Contains(t, all.GetDeploymentMap(), "node-a/monitor")
	assert.Contains(t, all.GetDeploymentMap(), "node-b/monitor")
	filtered, err := svc.ListActiveServiceDeployments(ctx, &pb.ListActiveServiceDeploymentsReq{NodeId: "node-a"})
	require.NoError(t, err)
	assert.Contains(t, filtered.GetDeploymentMap(), "monitor")
	assert.NotContains(t, filtered.GetDeploymentMap(), "node-a/monitor")
	deleted, err := svc.DeleteServiceDeployment(ctx, &pb.DeleteServiceDeploymentReq{NodeId: "node-a", ServiceName: "monitor"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, deleted.GetRetInfo().GetCode())
	other, err = svc.GetServiceDeployment(ctx, &pb.GetServiceDeploymentReq{NodeId: "node-b", ServiceName: "monitor"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, other.GetRetInfo().GetCode())
}

func TestGatewayNodeManagementRPCFiltersValidationAndHostFK(t *testing.T) {
	db := setupEmptySysDeployTestDB(t)
	svc := newTestService(db)
	ctx := context.Background()
	require.NoError(t, db.Exec(`INSERT INTO t_ssh_host(c_name,c_address,c_user) VALUES ('host-a','a.example','root')`).Error)
	var hostID int64
	require.NoError(t, db.Table("t_ssh_host").Select("c_id").Where("c_address = ?", "a.example").Scan(&hostID).Error)
	created, err := svc.CreateGatewayNode(ctx, &pb.CreateGatewayNodeReq{Node: &pb.GatewayNode{NodeId: "node-a", HostId: hostID, Name: "A", PublicAddress: "https://a.example", Status: "enabled"}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, created.GetRetInfo().GetCode())
	local, err := svc.CreateGatewayNode(ctx, &pb.CreateGatewayNodeReq{Node: &pb.GatewayNode{NodeId: "node-b", Name: "B", PublicAddress: "http://127.0.0.1:11000", Status: "disabled"}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, local.GetRetInfo().GetCode())
	list, err := svc.ListGatewayNodes(ctx, &pb.ListGatewayNodesReq{NodeId: "node-a", Status: "enabled"})
	require.NoError(t, err)
	require.Len(t, list.GetNodes(), 1)
	assert.Equal(t, hostID, list.GetNodes()[0].GetHostId())
	empty, err := svc.ListGatewayNodes(ctx, &pb.ListGatewayNodesReq{NodeId: "node-a", Status: "disabled"})
	require.NoError(t, err)
	assert.Empty(t, empty.GetNodes())
	updated, err := svc.UpdateGatewayNode(ctx, &pb.UpdateGatewayNodeReq{NodeId: "node-a", Node: &pb.GatewayNode{Name: "A2", PublicAddress: "https://a.example", Status: "disabled"}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, updated.GetRetInfo().GetCode())
	assert.Equal(t, "A2", updated.GetNode().GetName())
	for _, invalid := range []*pb.GatewayNode{
		{NodeId: "Bad ID", Name: "bad", PublicAddress: "https://bad.example", Status: "enabled"},
		{NodeId: "missing-name", PublicAddress: "https://bad.example", Status: "enabled"},
		{NodeId: "bad-http", Name: "bad", PublicAddress: "http://bad.example", Status: "enabled"},
		{NodeId: "bad-status", Name: "bad", PublicAddress: "https://bad.example", Status: "active"},
		{NodeId: "bad-host", HostId: hostID + 999, Name: "bad", PublicAddress: "https://bad.example", Status: "enabled"},
	} {
		rsp, err := svc.CreateGatewayNode(ctx, &pb.CreateGatewayNodeReq{Node: invalid})
		require.NoError(t, err)
		assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	}
	deleted, err := svc.DeleteGatewayNode(ctx, &pb.DeleteGatewayNodeReq{NodeId: "node-a"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, deleted.GetRetInfo().GetCode())
}

func TestGatewayNodePBConversionIncludesRuntimeFields(t *testing.T) {
	now := time.Date(2026, 7, 15, 8, 9, 10, 0, time.UTC)
	hostID := int64(42)
	node := &GatewayNode{NodeID: "node-a", HostID: &hostID, Name: "A", PublicAddress: "https://a.example", Status: "enabled", RouteHash: "route", AppliedRouteHash: "applied", RouteCount: 3, LastSeenAt: &now, LastError: "err", CreatedAt: &now, UpdatedAt: &now}
	item := gatewayNodeToPB(node)
	assert.Equal(t, "node-a", item.GetNodeId())
	assert.Equal(t, "A", item.GetName())
	assert.Equal(t, "https://a.example", item.GetPublicAddress())
	assert.Equal(t, "enabled", item.GetStatus())
	assert.Equal(t, hostID, item.GetHostId())
	assert.Equal(t, "route", item.GetRouteHash())
	assert.Equal(t, "applied", item.GetAppliedRouteHash())
	assert.Equal(t, int32(3), item.GetRouteCount())
	assert.Equal(t, now.Format(time.RFC3339), item.GetLastSeenAt())
	assert.Equal(t, "err", item.GetLastError())
	assert.Equal(t, now.Format(time.RFC3339), item.GetCreatedAt())
	assert.Equal(t, now.Format(time.RFC3339), item.GetUpdatedAt())
	roundTrip := pbToGatewayNode(item)
	require.NotNil(t, roundTrip.LastSeenAt)
	require.NotNil(t, roundTrip.CreatedAt)
	require.NotNil(t, roundTrip.UpdatedAt)
	assert.Equal(t, now, *roundTrip.LastSeenAt)
}

func TestSeedDefaultsBootstrapsConfiguredNodeAndAttachesMatchingHost(t *testing.T) {
	for _, tc := range []struct {
		name        string
		hostAddress string
		wantHost    bool
	}{{"match", defaultPublicHost, true}, {"no-match", "10.0.0.9", false}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MOOX_ADMIN_NODE_ID", "configured-node")
			db := setupEmptySysDeployTestDB(t)
			svc := newTestService(db)
			ctx := context.Background()
			require.NoError(t, db.Exec(`INSERT INTO t_ssh_host(c_name,c_address,c_user) VALUES ('host',?,'root')`, tc.hostAddress).Error)
			require.NoError(t, svc.SeedDefaults(ctx))
			node, err := svc.dao.GetGatewayNode(ctx, "configured-node")
			require.NoError(t, err)
			if tc.wantHost {
				assert.NotNil(t, node.HostID)
			} else {
				assert.Nil(t, node.HostID)
			}
			rows, _, err := svc.dao.List(ctx, ListFilter{NodeID: "configured-node"}, 0, 100)
			require.NoError(t, err)
			enabledIDs := map[string]bool{}
			for _, row := range rows {
				assert.NotContains(t, []string{"service_gateway", "service_gateway_internal"}, row.ServiceName)
				if row.GatewayEnabled {
					assert.Equal(t, "127.0.0.1", row.Host)
					assert.NotEmpty(t, row.GatewayServiceID)
					enabledIDs[row.GatewayServiceID] = true
				}
			}
			for _, serviceID := range []string{"collectmgr", "cloudnode", "factormgr", "strategymgr", "monitor", "hostagent"} {
				assert.True(t, enabledIDs[serviceID], "canonical gateway service %s missing", serviceID)
			}
			for _, serviceID := range []string{"secret", "sysdeploy", "trade_account", "trade_apikey", "trade_order", "trade_tradeop"} {
				assert.False(t, enabledIDs[serviceID], "sensitive gateway service %s exposed", serviceID)
			}
		})
	}
}

func TestSeedDefaultsRemovesPersistedObsoleteGatewayRows(t *testing.T) {
	t.Setenv("MOOX_ADMIN_NODE_ID", "configured-node")
	db := setupEmptySysDeployTestDB(t)
	svc := newTestService(db)
	ctx := context.Background()
	require.NoError(t, svc.dao.CreateGatewayNode(ctx, &GatewayNode{NodeID: "configured-node", Name: "configured-node", PublicAddress: "https://a.example", Status: "enabled"}))
	for _, name := range []string{"service_gateway", "service_gateway_internal"} {
		row := Deployment{NodeID: "configured-node", ServiceName: name, Host: "127.0.0.1", Port: 11001, Status: "active"}
		require.NoError(t, svc.dao.Create(ctx, &row))
	}
	require.NoError(t, svc.SeedDefaults(ctx))
	var count int64
	require.NoError(t, db.Model(&Deployment{}).Where("c_service_name IN ?", []string{"service_gateway", "service_gateway_internal"}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestReportGatewayStatusExactHeartbeatAndUnknownNode(t *testing.T) {
	dao := NewDAO(setupEmptySysDeployTestDB(t))
	ctx := context.Background()
	require.NoError(t, dao.CreateGatewayNode(ctx, &GatewayNode{NodeID: "node-a", Name: "A", PublicAddress: "https://a.example", Status: "enabled"}))
	injected := time.Date(2026, 7, 15, 1, 2, 3, 0, time.UTC)
	require.NoError(t, dao.ReportGatewayStatus(ctx, GatewayStatusReport{NodeID: "node-a", LastSeenAt: injected}))
	node, err := dao.GetGatewayNode(ctx, "node-a")
	require.NoError(t, err)
	require.NotNil(t, node.LastSeenAt)
	assert.True(t, injected.Equal(*node.LastSeenAt))
	assert.Error(t, dao.ReportGatewayStatus(ctx, GatewayStatusReport{NodeID: "missing", LastSeenAt: injected}))
	var count int64
	require.NoError(t, dbCountGatewayNodes(dao, &count))
	assert.Equal(t, int64(1), count)
}

func TestReportGatewayStatusIgnoresOutOfOrderReport(t *testing.T) {
	dao := NewDAO(setupEmptySysDeployTestDB(t))
	ctx := context.Background()
	require.NoError(t, dao.CreateGatewayNode(ctx, &GatewayNode{NodeID: "node-a", Name: "A", PublicAddress: "https://a.example", Status: "enabled"}))
	newer := time.Date(2026, 7, 15, 2, 0, 0, 0, time.UTC)
	older := newer.Add(-time.Minute)
	require.NoError(t, dao.ReportGatewayStatus(ctx, GatewayStatusReport{NodeID: "node-a", AppliedRouteHash: "new", RouteCount: 2, LastSeenAt: newer, LastError: "new-error"}))
	require.NoError(t, dao.ReportGatewayStatus(ctx, GatewayStatusReport{NodeID: "node-a", AppliedRouteHash: "old", RouteCount: 1, LastSeenAt: older, LastError: "old-error"}))
	node, err := dao.GetGatewayNode(ctx, "node-a")
	require.NoError(t, err)
	assert.Equal(t, "new", node.AppliedRouteHash)
	assert.Equal(t, int32(2), node.RouteCount)
	assert.Equal(t, "new-error", node.LastError)
	require.NotNil(t, node.LastSeenAt)
	assert.True(t, newer.Equal(*node.LastSeenAt))
}

func TestDisabledNodeStateChangesAndRestoresRouteHash(t *testing.T) {
	dao := NewDAO(setupEmptySysDeployTestDB(t))
	ctx := context.Background()
	require.NoError(t, dao.CreateGatewayNode(ctx, &GatewayNode{NodeID: "node-a", Name: "A", PublicAddress: "https://a.example", Status: "enabled"}))
	enabled, err := dao.CompileGatewaySnapshot(ctx, "node-a")
	require.NoError(t, err)
	require.NoError(t, dao.UpdateGatewayNode(ctx, "node-a", &GatewayNode{Name: "A", PublicAddress: "https://a.example", Status: "disabled"}))
	disabled, err := dao.CompileGatewaySnapshot(ctx, "node-a")
	require.NoError(t, err)
	assert.NotEqual(t, enabled.RouteHash, disabled.RouteHash)
	require.NoError(t, dao.UpdateGatewayNode(ctx, "node-a", &GatewayNode{Name: "A", PublicAddress: "https://a.example", Status: "enabled"}))
	reenabled, err := dao.CompileGatewaySnapshot(ctx, "node-a")
	require.NoError(t, err)
	assert.Equal(t, enabled.RouteHash, reenabled.RouteHash)
}

func TestSeedDefaultsSensitiveServicesCannotEnterSnapshot(t *testing.T) {
	t.Setenv("MOOX_ADMIN_NODE_ID", "node-a")
	svc := newTestService(setupEmptySysDeployTestDB(t))
	ctx := context.Background()
	require.NoError(t, svc.dao.CreateGatewayNode(ctx, &GatewayNode{NodeID: "node-a", Name: "A", PublicAddress: "https://a.example", Status: "enabled"}))
	require.NoError(t, svc.dao.Create(ctx, &Deployment{NodeID: "node-a", ServiceName: "sysdeploy", Protocol: "http", Host: "127.0.0.1", Port: 11109, GatewayPath: "trpc.moox.ops.SysDeploy", GatewayServiceID: "sysdeploy", GatewayEnabled: true, Status: "active"}))
	require.NoError(t, svc.SeedDefaults(ctx))
	snapshot, err := svc.CompileGatewaySnapshot(ctx, "node-a")
	require.NoError(t, err)
	ids := map[string]bool{}
	for _, route := range snapshot.Routes {
		ids[route.ServiceID] = true
	}
	for _, id := range []string{"secret", "sysdeploy", "trade_account", "trade_apikey", "trade_order", "trade_tradeop"} {
		assert.False(t, ids[id], "sensitive route %s compiled", id)
	}
}

func TestUniqueConstraintClassificationIsNarrow(t *testing.T) {
	assert.True(t, isUniqueConstraintError(errors.New("UNIQUE constraint failed: t.c")))
	assert.False(t, isUniqueConstraintError(errors.New("constraint failed: FOREIGN KEY constraint failed (787)")))
	assert.False(t, isUniqueConstraintError(errors.New("constraint failed: CHECK constraint failed (275)")))
	db := setupEmptySysDeployTestDB(t)
	fkErr := db.Create(&Deployment{NodeID: "missing", ServiceName: "svc"}).Error
	require.Error(t, fkErr)
	assert.False(t, isUniqueConstraintError(fkErr))
	checkErr := db.Create(&GatewayNode{NodeID: "node-a", Name: "A", PublicAddress: "https://a.example", Status: "invalid"}).Error
	require.Error(t, checkErr)
	assert.False(t, isUniqueConstraintError(checkErr))
}

func TestDefaultDeploymentsExposeOnlyMachineModuleManagers(t *testing.T) {
	allowed := map[string]string{"moox_collector": "collectmgr", "moox_cloudnode": "cloudnode", "moox_factor": "factormgr", "moox_strategy": "strategymgr", "moox_monitor": "monitor", "moox_hostagent": "hostagent"}
	sensitive := map[string]bool{"secret": true, "sysdeploy": true, "trade_account": true, "trade_apikey": true, "trade_order": true, "trade_tradeop": true}
	for _, row := range DefaultDeployments("node-a") {
		if serviceID, ok := allowed[row.ServiceName]; ok {
			assert.True(t, row.GatewayEnabled)
			assert.Equal(t, serviceID, row.GatewayServiceID)
			continue
		}
		if sensitive[row.ServiceName] {
			assert.False(t, row.GatewayEnabled)
			assert.Empty(t, row.GatewayServiceID)
		}
	}
}

func TestGatewayNodeRPCMapsMissingAndUnexpectedDatabaseErrors(t *testing.T) {
	db := setupEmptySysDeployTestDB(t)
	svc := newTestService(db)
	ctx := context.Background()
	missing, err := svc.UpdateGatewayNode(ctx, &pb.UpdateGatewayNodeReq{NodeId: "missing", Node: &pb.GatewayNode{Name: "missing", PublicAddress: "https://missing.example", Status: "enabled"}})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_NOT_FOUND, missing.GetRetInfo().GetCode())
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	failed, err := svc.CreateGatewayNode(ctx, &pb.CreateGatewayNodeReq{Node: &pb.GatewayNode{NodeId: "node-a", Name: "A", PublicAddress: "https://a.example", Status: "enabled"}})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INNER_ERR, failed.GetRetInfo().GetCode())
}

func dbCountGatewayNodes(dao *DAO, count *int64) error {
	return dao.db.Model(&GatewayNode{}).Count(count).Error
}

func TestDeploymentAndNodeListStatusFilters(t *testing.T) {
	dao := NewDAO(setupEmptySysDeployTestDB(t))
	svc := newTestService(dao.db)
	ctx := context.Background()
	for _, node := range []GatewayNode{{NodeID: "node-enabled", Name: "enabled", PublicAddress: "https://enabled.example", Status: "enabled"}, {NodeID: "node-disabled", Name: "disabled", PublicAddress: "https://disabled.example", Status: "disabled"}} {
		item := node
		require.NoError(t, dao.CreateGatewayNode(ctx, &item))
	}
	for _, status := range []string{"active", "disabled"} {
		row := Deployment{NodeID: "node-enabled", ServiceName: "svc-" + status, Host: "127.0.0.1", Port: 1000, Status: status}
		require.NoError(t, dao.Create(ctx, &row))
	}
	deploymentRsp, err := svc.ListServiceDeployments(ctx, &pb.ListServiceDeploymentsReq{Status: "disabled"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, deploymentRsp.GetRetInfo().GetCode())
	require.Len(t, deploymentRsp.GetDeployments(), 1)
	assert.Equal(t, "svc-disabled", deploymentRsp.GetDeployments()[0].GetServiceName())
	nodes, total, err := dao.ListGatewayNodes(ctx, GatewayNodeFilter{NodeID: "node-disabled", Status: "disabled"}, 0, 10)
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	require.Len(t, nodes, 1)
	assert.Equal(t, "node-disabled", nodes[0].NodeID)
}
