package sysdeploy

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayproxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompileGatewaySnapshot_IsNodeLocalDeterministicAndUsesExtraConfig(t *testing.T) {
	dao := NewDAO(setupSysDeployTestDB(t))
	ctx := context.Background()
	require.NoError(t, dao.CreateGatewayNode(ctx, &GatewayNode{NodeID: "node-a", Name: "A", PublicAddress: "https://a.example", Status: "enabled"}))
	require.NoError(t, dao.CreateGatewayNode(ctx, &GatewayNode{NodeID: "node-b", Name: "B", PublicAddress: "https://b.example", Status: "enabled"}))
	for _, row := range []Deployment{
		{NodeID: "node-a", ServiceName: "monitor", Protocol: "http", Host: "127.0.0.1", Port: 11410, GatewayPath: "trpc.moox.monitor.MonitorMgr", GatewayServiceID: "monitor", GatewayEnabled: true, Status: "active", ExtraConfig: `{"timeout_ms":9000,"max_body_bytes":12345,"gateway_methods":["*"],"gateway_callers":["*"]}`},
		{NodeID: "node-b", ServiceName: "monitor", Protocol: "http", Host: "127.0.0.1", Port: 21410, GatewayPath: "trpc.moox.monitor.MonitorMgr", GatewayServiceID: "monitor", GatewayEnabled: true, Status: "active"},
	} {
		require.NoError(t, dao.Create(ctx, &row))
	}
	first, err := dao.CompileGatewaySnapshot(ctx, "node-a")
	require.NoError(t, err)
	second, err := dao.CompileGatewaySnapshot(ctx, "node-a")
	require.NoError(t, err)
	require.Len(t, first.Routes, 1)
	assert.Equal(t, first.RouteHash, second.RouteHash)
	assert.Equal(t, int64(9000), first.Routes[0].TimeoutMS)
	assert.Equal(t, int64(12345), first.Routes[0].MaxBodyBytes)
	node, err := dao.GetGatewayNode(ctx, "node-a")
	require.NoError(t, err)
	assert.Equal(t, int32(1), node.RouteCount)
	assert.Equal(t, first.RouteHash, node.RouteHash)
}

func TestCompileGatewaySnapshot_DefaultsInvalidExtraAndDisabledNode(t *testing.T) {
	dao := NewDAO(setupSysDeployTestDB(t))
	ctx := context.Background()
	require.NoError(t, dao.CreateGatewayNode(ctx, &GatewayNode{NodeID: "node-a", Name: "A", PublicAddress: "https://a.example", Status: "enabled"}))
	row := Deployment{NodeID: "node-a", ServiceName: "svc", Protocol: "http", Host: "127.0.0.1", Port: 1000, GatewayPath: "trpc.test.Service", GatewayServiceID: "svc", GatewayEnabled: true, Status: "active", ExtraConfig: `{"gateway_methods":["*"],"gateway_callers":["*"]}`}
	require.NoError(t, dao.Create(ctx, &row))
	snapshot, err := dao.CompileGatewaySnapshot(ctx, "node-a")
	require.NoError(t, err)
	require.Len(t, snapshot.Routes, 1)
	assert.Equal(t, int64(5000), snapshot.Routes[0].TimeoutMS)
	assert.Equal(t, int64(4<<20), snapshot.Routes[0].MaxBodyBytes)
	require.NoError(t, dao.db.Model(&Deployment{}).Where("c_node_id = ? AND c_service_name = ?", "node-a", "svc").Update("c_extra_config", `{"timeout_ms":"bad"}`).Error)
	_, err = dao.CompileGatewaySnapshot(ctx, "node-a")
	assert.Error(t, err)
	require.NoError(t, dao.db.Model(&Deployment{}).Where("c_node_id = ? AND c_service_name = ?", "node-a", "svc").Update("c_extra_config", `{"timeout_ms":null}`).Error)
	_, err = dao.CompileGatewaySnapshot(ctx, "node-a")
	assert.Error(t, err)
	require.NoError(t, dao.UpdateGatewayNode(ctx, "node-a", &GatewayNode{NodeID: "node-a", Name: "A", PublicAddress: "https://a.example", Status: "disabled"}))
	disabled, err := dao.CompileGatewaySnapshot(ctx, "node-a")
	require.NoError(t, err)
	assert.True(t, disabled.Disabled)
	assert.Empty(t, disabled.Routes)
	assert.NotEmpty(t, disabled.RouteHash)
}

func TestFactorGatewayRouteUsesNativeTRPCListener(t *testing.T) {
	row := Deployment{
		Host: "127.0.0.1", Port: 11404,
		GatewayPath: "trpc.moox.factor.FactorMgr", GatewayServiceID: "factormgr",
	}
	routes, err := deploymentGatewayRoutes(row, routeExtraConfig{
		GatewayMethods: []string{"*"},
		GatewayCallers: []string{"*"},
	})
	require.NoError(t, err)
	require.Len(t, routes, 1)
	assert.Equal(t, "127.0.0.1:11403", routes[0].Address)
}

func TestStrategyGatewayRouteUsesNativeTRPCListener(t *testing.T) {
	row := Deployment{
		Host: "127.0.0.1", Port: 11433,
		GatewayPath: "trpc.moox.strategy.StrategyMgr", GatewayServiceID: "strategymgr",
	}
	routes, err := deploymentGatewayRoutes(row, routeExtraConfig{
		GatewayMethods: []string{"GetStrategy", "CreateStrategyInstance"},
		GatewayCallers: []string{"admin-gateway", "moox-cli"},
	})
	require.NoError(t, err)
	require.Len(t, routes, 1)
	assert.Equal(t, "127.0.0.1:11430", routes[0].Address)
}

func TestReportGatewayStatus_UpdatesHeartbeat(t *testing.T) {
	dao := NewDAO(setupSysDeployTestDB(t))
	ctx := context.Background()
	require.NoError(t, dao.CreateGatewayNode(ctx, &GatewayNode{NodeID: "node-a", Name: "A", PublicAddress: "https://a.example", Status: "enabled"}))
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, dao.ReportGatewayStatus(ctx, GatewayStatusReport{NodeID: "node-a", AppliedRouteHash: "hash", RouteCount: 7, LastSeenAt: now, LastError: "boom"}))
	node, err := dao.GetGatewayNode(ctx, "node-a")
	require.NoError(t, err)
	assert.Equal(t, "hash", node.AppliedRouteHash)
	assert.Equal(t, int32(7), node.RouteCount)
	assert.Equal(t, "boom", node.LastError)
	require.NotNil(t, node.LastSeenAt)
}

func TestValidateDeployment_GatewayRouteRules(t *testing.T) {
	base := Deployment{NodeID: "node-a", ServiceName: "svc", Protocol: "http", Host: "127.0.0.1", Port: 1234, GatewayPath: "trpc.test.Service", GatewayServiceID: "svc", GatewayEnabled: true, Status: "active", ExtraConfig: `{"gateway_methods":["*"],"gateway_callers":["*"]}`}
	assert.NoError(t, validateDeployment(&base))
	for _, mutate := range []func(*Deployment){
		func(d *Deployment) { d.Host = "10.0.0.1" },
		func(d *Deployment) { d.Protocol = "https" },
		func(d *Deployment) { d.GatewayServiceID = "Bad" },
		func(d *Deployment) { d.GatewayPath = "/bad" },
	} {
		item := base
		mutate(&item)
		assert.Error(t, validateDeployment(&item))
	}
	_ = gatewayproxy.Route{}
}

func TestDAO_DeploymentIdentityIsScopedToNode(t *testing.T) {
	dao := NewDAO(setupSysDeployTestDB(t))
	ctx := context.Background()
	for _, id := range []string{"node-a", "node-b"} {
		require.NoError(t, dao.CreateGatewayNode(ctx, &GatewayNode{NodeID: id, Name: id, PublicAddress: "https://" + id + ".example", Status: "enabled"}))
	}
	makeRow := func(nodeID string) Deployment {
		return Deployment{NodeID: nodeID, ServiceName: "moox_monitor", Protocol: "http", Host: "127.0.0.1", Port: 11410, GatewayPath: "trpc.moox.monitor.MonitorMgr", GatewayServiceID: "monitor", GatewayEnabled: true, Status: "active"}
	}
	first := makeRow("node-a")
	require.NoError(t, dao.Create(ctx, &first))
	duplicate := makeRow("node-a")
	assert.Error(t, dao.Create(ctx, &duplicate))
	duplicateGatewayID := makeRow("node-a")
	duplicateGatewayID.ServiceName = "other_monitor"
	assert.Error(t, dao.Create(ctx, &duplicateGatewayID))
	other := makeRow("node-b")
	require.NoError(t, dao.Create(ctx, &other))
	rows, _, err := dao.List(ctx, ListFilter{NodeID: "node-a"}, 0, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	enabled := true
	rows, _, err = dao.List(ctx, ListFilter{GatewayEnabled: &enabled}, 0, 10)
	require.NoError(t, err)
	assert.Len(t, rows, 2)
}

func TestGatewayNodeCRUDAndPBConversion(t *testing.T) {
	dao := NewDAO(setupSysDeployTestDB(t))
	ctx := context.Background()
	node := &GatewayNode{NodeID: "node-a", Name: "A", PublicAddress: "https://a.example", Status: "enabled"}
	require.NoError(t, dao.CreateGatewayNode(ctx, node))
	row, err := dao.GetGatewayNode(ctx, "node-a")
	require.NoError(t, err)
	assert.Equal(t, "A", row.Name)
	require.NoError(t, dao.UpdateGatewayNode(ctx, "node-a", &GatewayNode{Name: "A2", PublicAddress: "https://a.example", Status: "disabled"}))
	row, err = dao.GetGatewayNode(ctx, "node-a")
	require.NoError(t, err)
	assert.Equal(t, "A2", row.Name)
	converted := pbToGatewayNode(gatewayNodeToPB(row))
	assert.Equal(t, row.NodeID, converted.NodeID)
	assert.Equal(t, row.Status, converted.Status)
	require.NoError(t, dao.DeleteGatewayNode(ctx, "node-a"))
	_, err = dao.GetGatewayNode(ctx, "node-a")
	assert.Error(t, err)
}

func TestEndpointMap_UsesCompositeKeysWithoutNodeFilter(t *testing.T) {
	rows := []Deployment{{NodeID: "a", ServiceName: "monitor"}, {NodeID: "b", ServiceName: "monitor"}}
	items := endpointMap(rows, true)
	assert.Contains(t, items, "a/monitor")
	assert.Contains(t, items, "b/monitor")
}

func TestIsSQLiteLockError(t *testing.T) {
	for _, err := range []error{
		fmt.Errorf("database is locked"),
		fmt.Errorf("SQLITE_BUSY: database table is locked"),
	} {
		assert.True(t, isSQLiteLockError(err))
	}
	assert.False(t, isSQLiteLockError(fmt.Errorf("constraint failed")))
}
