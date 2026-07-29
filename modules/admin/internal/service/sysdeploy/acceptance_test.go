package sysdeploy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/database"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/gatewayproxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newTestService(db *gorm.DB, adminNodeID string) *ServiceImpl {
	svc := NewService(&database.Manager{}, adminNodeID)
	svc.dao = NewDAO(db)
	return svc
}

func TestCompileGatewaySnapshot_ExcludesDisabledDeployment(t *testing.T) {
	dao := NewDAO(setupEmptySysDeployTestDB(t))
	ctx := context.Background()
	require.NoError(t, dao.CreateGatewayNode(ctx, &GatewayNode{NodeID: "node-a", Name: "A", PublicAddress: "https://a.example", Status: "enabled"}))
	for _, row := range []Deployment{
		{NodeID: "node-a", ServiceName: "active", Protocol: "http", Host: "127.0.0.1", Port: 1001, GatewayPath: "trpc.test.Active", GatewayServiceID: "active", GatewayEnabled: true, Status: "active", ExtraConfig: `{"gateway_methods":["*"],"gateway_callers":["*"]}`},
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
	svc := newTestService(db, testAdminNodeID)
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
	svc := newTestService(db, testAdminNodeID)
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
			db := setupEmptySysDeployTestDB(t)
			svc := newTestService(db, "configured-node")
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
				assert.NotEqual(t, "service_gateway_internal", row.ServiceName)
				if row.GatewayEnabled {
					assert.Equal(t, "127.0.0.1", row.Host)
					assert.NotEmpty(t, row.GatewayServiceID)
					enabledIDs[row.GatewayServiceID] = true
				}
			}
			for _, serviceID := range []string{"collectmgr", "cloudnode", "factormgr", "strategymgr", "monitor", "hostagent", "sysdeploy", "secret"} {
				assert.True(t, enabledIDs[serviceID], "canonical gateway service %s missing", serviceID)
			}
			for _, serviceID := range []string{"trade_exchange_account", "trade_execution"} {
				assert.False(t, enabledIDs[serviceID], "sensitive gateway service %s exposed", serviceID)
			}
		})
	}
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
	svc := newTestService(setupEmptySysDeployTestDB(t), "node-a")
	ctx := context.Background()
	require.NoError(t, svc.dao.CreateGatewayNode(ctx, &GatewayNode{NodeID: "node-a", Name: "A", PublicAddress: "https://a.example", Status: "enabled"}))
	require.NoError(t, svc.dao.Create(ctx, &Deployment{NodeID: "node-a", ServiceName: "sysdeploy", Protocol: "http", Host: "127.0.0.1", Port: 11109, GatewayPath: "trpc.moox.ops.SysDeploy", GatewayServiceID: "sysdeploy", GatewayEnabled: true, Status: "active", ExtraConfig: `{"gateway_methods":["ListActiveServiceDeployments"],"gateway_callers":["*"]}`}))
	require.NoError(t, svc.SeedDefaults(ctx))
	snapshot, err := svc.CompileGatewaySnapshot(ctx, "node-a")
	require.NoError(t, err)
	ids := map[string]bool{}
	for _, route := range snapshot.Routes {
		ids[route.ServiceID] = true
	}
	for _, id := range []string{"trade_exchange_account", "trade_execution"} {
		assert.False(t, ids[id], "sensitive route %s compiled", id)
	}
}

func TestSeedDefaultsDeletesOnlyNodeScopedObsoleteTradeDeployments(t *testing.T) {
	db := setupEmptySysDeployTestDB(t)
	ctx := context.Background()
	for _, nodeID := range []string{"node-a", "node-b"} {
		require.NoError(t, db.Create(&GatewayNode{
			NodeID: nodeID, Name: nodeID,
			PublicAddress: "https://" + nodeID + ".example", Status: "enabled",
		}).Error)
	}
	for _, nodeID := range []string{"node-a", "node-b"} {
		for _, serviceName := range obsoleteTradeDeploymentNames {
			require.NoError(t, db.Create(&Deployment{
				NodeID: nodeID, ServiceName: serviceName, ServiceKind: "trade",
				Protocol: "http", Host: "127.0.0.1", Port: 11200,
				GatewayPath: "trpc.moox.trade.Legacy", Scope: "internal", Status: "active",
			}).Error)
		}
	}

	svc := newTestService(db, "node-a")
	require.NoError(t, svc.SeedDefaults(ctx))

	var nodeAObsolete, nodeBObsolete int64
	require.NoError(t, db.Model(&Deployment{}).
		Where("c_node_id = ? AND c_service_name IN ?", "node-a", obsoleteTradeDeploymentNames).
		Count(&nodeAObsolete).Error)
	require.NoError(t, db.Model(&Deployment{}).
		Where("c_node_id = ? AND c_service_name IN ?", "node-b", obsoleteTradeDeploymentNames).
		Count(&nodeBObsolete).Error)
	assert.Zero(t, nodeAObsolete)
	assert.Equal(t, int64(len(obsoleteTradeDeploymentNames)), nodeBObsolete)

	var canonical int64
	require.NoError(t, db.Model(&Deployment{}).
		Where("c_node_id = ? AND c_service_name IN ?", "node-a", []string{"trade_exchange_account", "trade_execution"}).
		Count(&canonical).Error)
	assert.Equal(t, int64(2), canonical)
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
	allowed := map[string]string{"moox_collector": "collectmgr", "moox_cloudnode": "cloudnode", "moox_factor": "factormgr", "moox_strategy": "strategymgr", "moox_monitor": "monitor", "moox_hostagent": "hostagent", "sysdeploy": "sysdeploy", "secret": "secret"}
	sensitive := map[string]bool{"trade_exchange_account": true, "trade_execution": true}
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

func TestDefaultSysdeployRouteAllowsBoundedInventoryLookup(t *testing.T) {
	svc := newTestService(setupEmptySysDeployTestDB(t), "node-a")
	ctx := context.Background()
	require.NoError(t, svc.SeedDefaults(ctx))
	snapshot, err := svc.CompileGatewaySnapshot(ctx, "node-a")
	require.NoError(t, err)
	var sysdeployRoute gatewayproxy.Route
	for _, route := range snapshot.Routes {
		if route.ServiceID == "sysdeploy" {
			sysdeployRoute = route
		}
	}
	require.Equal(t, "sysdeploy", sysdeployRoute.ServiceID)
	assert.True(t, sysdeployRoute.AllowsMethod("ListActiveServiceDeployments"))
	assert.True(t, sysdeployRoute.AllowsMethod("ListServiceDeployments"))
	assert.False(t, sysdeployRoute.AllowsMethod("CreateGatewayNode"))
	assert.True(t, sysdeployRoute.AllowsCaller("collector"))
	assert.Equal(t, []string{"ListActiveServiceDeployments", "ListServiceDeployments"}, sysdeployRoute.AllowedMethods)
	rsp, err := svc.GetGatewayNodeRoutes(ctx, &pb.GetGatewayNodeRoutesReq{NodeId: "node-a"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	for _, route := range rsp.GetRoutes() {
		if route.GetServiceId() == "sysdeploy" {
			assert.Equal(t, []string{"ListActiveServiceDeployments", "ListServiceDeployments"}, route.GetAllowedMethods())
		}
	}
	var secretRoute gatewayproxy.Route
	for _, route := range snapshot.Routes {
		if route.ServiceID == "secret" {
			secretRoute = route
		}
	}
	require.Equal(t, "secret", secretRoute.ServiceID)
	assert.Equal(t, []string{"ListSecrets", "RevealSecret"}, secretRoute.AllowedMethods)
	assert.True(t, secretRoute.AllowsMethod("ListSecrets"))
	assert.True(t, secretRoute.AllowsMethod("RevealSecret"))
	assert.False(t, secretRoute.AllowsMethod("DeleteSecret"))
	assert.False(t, secretRoute.AllowsMethod("CreateSecret"))
}

func TestSensitiveGatewayDeploymentsRequireNonemptyMethodsOnCreateUpdateAndCompile(t *testing.T) {
	db := setupEmptySysDeployTestDB(t)
	svc := newTestService(db, testAdminNodeID)
	ctx := context.Background()
	require.NoError(t, svc.dao.CreateGatewayNode(ctx, &GatewayNode{NodeID: "node-a", Name: "A", PublicAddress: "https://a.example", Status: "enabled"}))
	makePB := func(extra string) *pb.ServiceDeployment {
		return &pb.ServiceDeployment{NodeId: "node-a", ServiceName: "sysdeploy", Protocol: "http", Host: "127.0.0.1", Port: 11109, GatewayPath: "trpc.moox.ops.SysDeploy", GatewayServiceId: "sysdeploy", GatewayEnabled: true, Status: "active", ExtraConfig: extra}
	}
	for _, extra := range []string{`{}`, `{"gateway_methods":[]}`} {
		rsp, err := svc.CreateServiceDeployment(ctx, &pb.CreateServiceDeploymentReq{Deployment: makePB(extra)})
		require.NoError(t, err)
		assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	}
	created, err := svc.CreateServiceDeployment(ctx, &pb.CreateServiceDeploymentReq{Deployment: makePB(`{"gateway_methods":["ListActiveServiceDeployments"],"gateway_callers":["*"]}`)})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, created.GetRetInfo().GetCode())
	updated, err := svc.UpdateServiceDeployment(ctx, &pb.UpdateServiceDeploymentReq{NodeId: "node-a", ServiceName: "sysdeploy", Deployment: makePB(`{}`)})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, updated.GetRetInfo().GetCode())
	require.NoError(t, db.Model(&Deployment{}).Where("c_node_id = ? AND c_service_name = ?", "node-a", "sysdeploy").Update("c_extra_config", `{}`).Error)
	_, err = svc.CompileGatewaySnapshot(ctx, "node-a")
	var configErr *RouteConfigError
	assert.ErrorAs(t, err, &configErr)
	assert.ErrorIs(t, err, ErrInvalidGatewayRoute)
}

func TestSensitiveGatewayPathsRequireMethodsEvenWithAliasServiceID(t *testing.T) {
	for _, tc := range []struct{ name, servicePath, alias, allowed string }{
		{name: "sysdeploy alias", servicePath: "trpc.moox.ops.SysDeploy", alias: "ops_alias", allowed: "ListActiveServiceDeployments"},
		{name: "secret alias", servicePath: "trpc.moox.ops.SecretMgr", alias: "secret_alias", allowed: "ListSecrets"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupEmptySysDeployTestDB(t)
			svc := newTestService(db, testAdminNodeID)
			ctx := context.Background()
			require.NoError(t, svc.dao.CreateGatewayNode(ctx, &GatewayNode{NodeID: "node-a", Name: "A", PublicAddress: "https://a.example", Status: "enabled"}))
			makePB := func(extra string) *pb.ServiceDeployment {
				return &pb.ServiceDeployment{NodeId: "node-a", ServiceName: "aliased", Protocol: "http", Host: "127.0.0.1", Port: 11109, GatewayPath: tc.servicePath, GatewayServiceId: tc.alias, GatewayEnabled: true, Status: "active", ExtraConfig: extra}
			}
			created, err := svc.CreateServiceDeployment(ctx, &pb.CreateServiceDeploymentReq{Deployment: makePB(`{}`)})
			require.NoError(t, err)
			assert.Equal(t, pb.ErrorCode_INVALID_PARAM, created.GetRetInfo().GetCode())
			created, err = svc.CreateServiceDeployment(ctx, &pb.CreateServiceDeploymentReq{Deployment: makePB(`{"gateway_methods":["` + tc.allowed + `"],"gateway_callers":["*"]}`)})
			require.NoError(t, err)
			require.Equal(t, pb.ErrorCode_SUCCESS, created.GetRetInfo().GetCode())
			updated, err := svc.UpdateServiceDeployment(ctx, &pb.UpdateServiceDeploymentReq{NodeId: "node-a", ServiceName: "aliased", Deployment: makePB(`{"gateway_methods":[]}`)})
			require.NoError(t, err)
			assert.Equal(t, pb.ErrorCode_INVALID_PARAM, updated.GetRetInfo().GetCode())
			require.NoError(t, db.Model(&Deployment{}).Where("c_node_id = ? AND c_service_name = ?", "node-a", "aliased").Update("c_extra_config", `{}`).Error)
			_, err = svc.CompileGatewaySnapshot(ctx, "node-a")
			assert.ErrorIs(t, err, ErrInvalidGatewayRoute)
		})
	}
}

func TestCompileGatewaySnapshotGatewayMethodsStrictAndNormalized(t *testing.T) {
	dao := NewDAO(setupEmptySysDeployTestDB(t))
	ctx := context.Background()
	require.NoError(t, dao.CreateGatewayNode(ctx, &GatewayNode{NodeID: "node-a", Name: "A", PublicAddress: "https://a.example", Status: "enabled"}))
	row := Deployment{NodeID: "node-a", ServiceName: "sysdeploy", Protocol: "http", Host: "127.0.0.1", Port: 11109, GatewayPath: "trpc.moox.ops.SysDeploy", GatewayServiceID: "sysdeploy", GatewayEnabled: true, Status: "active", ExtraConfig: `{"gateway_methods":["ListActiveServiceDeployments","ListActiveServiceDeployments"],"gateway_callers":["*"]}`}
	require.NoError(t, dao.Create(ctx, &row))
	snapshot, err := dao.CompileGatewaySnapshot(ctx, "node-a")
	require.NoError(t, err)
	assert.Equal(t, []string{"ListActiveServiceDeployments"}, snapshot.Routes[0].AllowedMethods)
	for _, invalid := range []string{`{"gateway_methods":"ListActiveServiceDeployments"}`, `{"gateway_methods":[1]}`, `{"gateway_methods":["../Delete"]}`} {
		require.NoError(t, dao.db.Model(&Deployment{}).Where("c_id = ?", row.ID).Update("c_extra_config", invalid).Error)
		_, err := dao.CompileGatewaySnapshot(ctx, "node-a")
		assert.Error(t, err)
	}
}

func TestGatewayNodeRPCMapsMissingAndUnexpectedDatabaseErrors(t *testing.T) {
	db := setupEmptySysDeployTestDB(t)
	svc := newTestService(db, testAdminNodeID)
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
	getDeployment, err := svc.GetServiceDeployment(ctx, &pb.GetServiceDeploymentReq{NodeId: "node-a", ServiceName: "svc"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INNER_ERR, getDeployment.GetRetInfo().GetCode())
	createDeployment, err := svc.CreateServiceDeployment(ctx, &pb.CreateServiceDeploymentReq{Deployment: &pb.ServiceDeployment{NodeId: "node-a", ServiceName: "svc", Protocol: "http", Host: "127.0.0.1", Port: 1000, Status: "active"}})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INNER_ERR, createDeployment.GetRetInfo().GetCode())
	routes, err := svc.GetGatewayNodeRoutes(ctx, &pb.GetGatewayNodeRoutesReq{NodeId: "node-a"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INNER_ERR, routes.GetRetInfo().GetCode())
}

func TestGetGatewayNodeRoutesClassifiesMissingAndRouteConfigErrors(t *testing.T) {
	db := setupEmptySysDeployTestDB(t)
	svc := newTestService(db, testAdminNodeID)
	ctx := context.Background()
	missing, err := svc.GetGatewayNodeRoutes(ctx, &pb.GetGatewayNodeRoutesReq{NodeId: "missing"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_NOT_FOUND, missing.GetRetInfo().GetCode())
	require.NoError(t, svc.dao.CreateGatewayNode(ctx, &GatewayNode{NodeID: "node-a", Name: "A", PublicAddress: "https://a.example", Status: "enabled"}))
	row := Deployment{NodeID: "node-a", ServiceName: "svc", Protocol: "http", Host: "127.0.0.1", Port: 1000, GatewayPath: "trpc.test.Service", GatewayServiceID: "svc", GatewayEnabled: true, Status: "active", ExtraConfig: `{"gateway_methods":"bad"}`}
	require.NoError(t, svc.dao.Create(ctx, &row))
	invalid, err := svc.GetGatewayNodeRoutes(ctx, &pb.GetGatewayNodeRoutesReq{NodeId: "node-a"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, invalid.GetRetInfo().GetCode())
}

func dbCountGatewayNodes(dao *DAO, count *int64) error {
	return dao.db.Model(&GatewayNode{}).Count(count).Error
}

func TestDeploymentAndNodeListStatusFilters(t *testing.T) {
	dao := NewDAO(setupEmptySysDeployTestDB(t))
	svc := newTestService(dao.db, testAdminNodeID)
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
