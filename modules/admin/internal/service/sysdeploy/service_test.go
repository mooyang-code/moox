package sysdeploy

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/admin/internal/service/database"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const testAdminNodeID = "gateway-gz-122"

func setupSysDeployTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupEmptySysDeployTestDB(t)
	require.NoError(t, db.Create(&GatewayNode{NodeID: testAdminNodeID, Name: "local", PublicAddress: "https://127.0.0.1", Status: "enabled"}).Error)
	return db
}

func setupEmptySysDeployTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(schema.AdminSQL()).Error)
	return db
}

func TestServiceImpl_SeedDefaults_ShouldInsertRows(t *testing.T) {
	db := setupSysDeployTestDB(t)
	svc := NewService(&database.Manager{}, testAdminNodeID)
	svc.dao = NewDAO(db)

	require.NoError(t, svc.SeedDefaults(context.Background()))

	rows, total, err := svc.dao.List(context.Background(), ListFilter{}, 0, 100)
	require.NoError(t, err)
	assert.Greater(t, total, int64(0))
	assert.NotEmpty(t, rows)
	node, err := svc.dao.GetGatewayNode(context.Background(), testAdminNodeID)
	require.NoError(t, err)
	assert.Equal(t, "enabled", node.Status)
	for _, row := range rows {
		assert.Equal(t, testAdminNodeID, row.NodeID)
		assert.NotEqual(t, "service_gateway_internal", row.ServiceName)
	}
}

func TestServiceImpl_SeedDefaults_BackfillsSkillReadRouteInLegacyStorageDeployment(t *testing.T) {
	db := setupSysDeployTestDB(t)
	svc := NewService(&database.Manager{}, testAdminNodeID)
	svc.dao = NewDAO(db)
	var legacy Deployment
	for _, item := range DefaultDeployments(testAdminNodeID) {
		if item.ServiceName == "storage-primary" {
			legacy = item
			break
		}
	}
	require.NotEmpty(t, legacy.ServiceName)
	legacy.ExtraConfig = `{"gateway_methods":["GetSpace"],"gateway_callers":["admin-gateway"],"gateway_routes":[{"service_path":"trpc.moox.storage.PrimaryStore","port":20102,"gateway_methods":["UpsertFields","ReadFields","ReadTimeSeriesRows","ReadRecordRows","ReportDatasetPeriodCollected","AppendDatasetSyncPoint","WaitViewSyncPoint","ReportFactorPeriodComputed","GetFactorPeriodComputed","OperatorAudit"],"gateway_callers":["admin-gateway","collector","factor","monitor","archive","storage-view","moox-skill"],"owner":"ops"},{"service_path":"trpc.moox.custom.Operator","port":29999,"gateway_methods":["CustomRead"],"gateway_callers":["operator"],"owner":"ops"}]}`
	require.NoError(t, svc.dao.Create(context.Background(), &legacy))

	require.NoError(t, svc.SeedDefaults(context.Background()))
	upgraded, err := svc.dao.Get(context.Background(), testAdminNodeID, "storage-primary")
	require.NoError(t, err)
	var extra struct {
		GatewayRoutes []map[string]any `json:"gateway_routes"`
	}
	require.NoError(t, json.Unmarshal([]byte(upgraded.ExtraConfig), &extra))
	var general, readOnly, custom map[string]any
	for _, route := range extra.GatewayRoutes {
		methods, _ := route["gateway_methods"].([]any)
		switch {
		case route["service_path"] == "trpc.moox.storage.PrimaryStore" && containsAny(methods, "UpsertFields"):
			general = route
		case route["service_path"] == "trpc.moox.storage.PrimaryStore" && len(methods) == 1 && methods[0] == "ReadTimeSeriesRows":
			readOnly = route
		case route["service_path"] == "trpc.moox.custom.Operator":
			custom = route
		}
	}
	require.NotNil(t, general)
	require.NotContains(t, general["gateway_methods"], "ReadTimeSeriesRows")
	require.NotContains(t, general["gateway_callers"], "moox-skill")
	require.Contains(t, general["gateway_methods"], "OperatorAudit")
	require.Equal(t, "ops", general["owner"])
	require.Equal(t, []any{"ReadTimeSeriesRows"}, readOnly["gateway_methods"])
	require.Contains(t, readOnly["gateway_callers"], "moox-skill")
	require.Equal(t, "ops", custom["owner"])
	snapshot, err := svc.dao.CompileGatewaySnapshot(context.Background(), testAdminNodeID)
	require.NoError(t, err)
	for _, route := range snapshot.Routes {
		if route.ServiceID == "storage-primary" && route.AllowsMethod("UpsertFields") {
			require.False(t, route.AllowsCaller("moox-skill"))
		}
	}
}

func containsAny(items []any, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func TestServiceImpl_SeedDefaults_RemovesObsoleteTradeDeployments(t *testing.T) {
	db := setupSysDeployTestDB(t)
	svc := NewService(&database.Manager{}, testAdminNodeID)
	svc.dao = NewDAO(db)
	ctx := context.Background()
	for _, name := range obsoleteDefaultDeploymentNames {
		require.NoError(t, svc.dao.Create(ctx, &Deployment{
			NodeID: testAdminNodeID, ServiceName: name, Host: "127.0.0.1", Port: 11200, Status: "active",
		}))
	}

	require.NoError(t, svc.SeedDefaults(ctx))
	for _, name := range obsoleteDefaultDeploymentNames {
		_, err := svc.dao.Get(ctx, testAdminNodeID, name)
		assert.ErrorIs(t, err, gorm.ErrRecordNotFound, name)
	}
	row, err := svc.dao.Get(ctx, testAdminNodeID, "trade_console")
	require.NoError(t, err)
	assert.Equal(t, int32(11200), row.Port)
	assert.Equal(t, "trpc.moox.trade.TradeConsoleService", row.GatewayPath)
}

func TestServiceImpl_CreateServiceDeployment_InvalidParam_ShouldReturnError(t *testing.T) {
	db := setupSysDeployTestDB(t)
	svc := NewService(&database.Manager{}, testAdminNodeID)
	svc.dao = NewDAO(db)

	rsp, err := svc.CreateServiceDeployment(context.Background(), &pb.CreateServiceDeploymentReq{
		Deployment: &pb.ServiceDeployment{ServiceName: "svc-a"},
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestServiceImpl_CreateAndGetServiceDeployment_ValidInput_ShouldSucceed(t *testing.T) {
	db := setupSysDeployTestDB(t)
	svc := NewService(&database.Manager{}, testAdminNodeID)
	svc.dao = NewDAO(db)

	createRsp, err := svc.CreateServiceDeployment(context.Background(), &pb.CreateServiceDeploymentReq{
		Deployment: &pb.ServiceDeployment{
			NodeId:      testAdminNodeID,
			ServiceName: "test_svc",
			Host:        "127.0.0.1",
			Port:        18080,
			GatewayPath: "trpc.moox.test.Service",
			Status:      "active",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, createRsp.GetRetInfo().GetCode())

	getRsp, err := svc.GetServiceDeployment(context.Background(), &pb.GetServiceDeploymentReq{NodeId: testAdminNodeID, ServiceName: "test_svc"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, getRsp.GetRetInfo().GetCode())
	assert.Equal(t, "test_svc", getRsp.GetDeployment().GetServiceName())
}

func TestServiceImpl_DeleteServiceDeployment_MissingName_ShouldReturnInvalidParam(t *testing.T) {
	db := setupSysDeployTestDB(t)
	svc := NewService(&database.Manager{}, testAdminNodeID)
	svc.dao = NewDAO(db)

	rsp, err := svc.DeleteServiceDeployment(context.Background(), &pb.DeleteServiceDeploymentReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestServiceImpl_ResolveAdminServiceDetail_RequiresMatchingActiveNode(t *testing.T) {
	db := setupSysDeployTestDB(t)
	svc := NewService(&database.Manager{}, "admin-node-b")
	svc.dao = NewDAO(db)
	ctx := context.Background()
	require.NoError(t, svc.dao.CreateGatewayNode(ctx, &GatewayNode{
		NodeID: "admin-node-b", Name: "admin-b", PublicAddress: "https://admin-b.example", Status: "enabled",
	}))
	require.NoError(t, svc.dao.Create(ctx, &Deployment{
		NodeID:      "admin-node-b",
		ServiceName: "moox_trade",
		Host:        "127.0.0.1",
		Port:        11210,
		GatewayPath: "trpc.moox.trade.Trade",
		Status:      "active",
	}))

	detail, ok := svc.ResolveAdminServiceDetail(ctx, "admin-node-b", "trade")
	assert.True(t, ok)
	assert.Equal(t, "127.0.0.1:11210", detail.Address)
	assert.Equal(t, "trpc.moox.trade.Trade", detail.Path)

	require.NoError(t, svc.dao.CreateGatewayNode(ctx, &GatewayNode{
		NodeID: "admin-node-a", Name: "admin-a", PublicAddress: "https://admin-a.example", Status: "enabled",
	}))
	require.NoError(t, svc.dao.Create(ctx, &Deployment{
		NodeID: "admin-node-a", ServiceName: "moox_trade", Host: "127.0.0.1", Port: 11211,
		GatewayPath: "trpc.moox.trade.Trade", Status: "active",
	}))
	_, ok = svc.ResolveAdminServiceDetail(ctx, "admin-node-a", "trade")
	assert.False(t, ok)

	require.NoError(t, svc.dao.Update(ctx, "admin-node-b", "moox_trade", &Deployment{
		NodeID: "admin-node-b", ServiceName: "moox_trade", Host: "127.0.0.1", Port: 11210,
		GatewayPath: "trpc.moox.trade.Trade", Status: "disabled",
	}))
	_, ok = svc.ResolveAdminServiceDetail(ctx, "admin-node-b", "trade")
	assert.False(t, ok)
}

func TestServiceImpl_ListActiveServiceDeployments_ActiveRows_ShouldReturnEndpoints(t *testing.T) {
	db := setupSysDeployTestDB(t)
	svc := NewService(&database.Manager{}, testAdminNodeID)
	svc.dao = NewDAO(db)
	require.NoError(t, svc.dao.Create(context.Background(), &Deployment{
		NodeID:      testAdminNodeID,
		ServiceName: "svc_active",
		Host:        "127.0.0.1",
		Port:        19191,
		GatewayPath: "trpc.moox.test.Active",
		Status:      "active",
	}))

	rsp, err := svc.ListActiveServiceDeployments(context.Background(), &pb.ListActiveServiceDeploymentsReq{NodeId: testAdminNodeID})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Contains(t, rsp.GetDeploymentMap(), "svc_active")
}

func TestEndpointMap_MultipleRows_ShouldIndexByServiceName(t *testing.T) {
	rows := []Deployment{
		{ServiceName: "a", Host: "127.0.0.1", Port: 1},
		{ServiceName: "b", Host: "127.0.0.1", Port: 2},
	}
	m := endpointMap(rows, false)
	assert.Len(t, m, 2)
	assert.Equal(t, "a", m["a"].ServiceName)
}

func TestServiceImpl_ListServiceDeployments_DefaultPage_ShouldReturnRows(t *testing.T) {
	db := setupSysDeployTestDB(t)
	svc := NewService(&database.Manager{}, testAdminNodeID)
	svc.dao = NewDAO(db)
	require.NoError(t, svc.SeedDefaults(context.Background()))

	rsp, err := svc.ListServiceDeployments(context.Background(), &pb.ListServiceDeploymentsReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.NotEmpty(t, rsp.GetDeployments())
}

func TestFormatTime_ZeroTime_ShouldReturnEmpty(t *testing.T) {
	assert.Empty(t, formatTime(time.Time{}))
}

func TestServiceImpl_GetServiceDeployment_NotFound_ShouldReturnNotFound(t *testing.T) {
	db := setupSysDeployTestDB(t)
	svc := NewService(&database.Manager{}, testAdminNodeID)
	svc.dao = NewDAO(db)

	rsp, err := svc.GetServiceDeployment(context.Background(), &pb.GetServiceDeploymentReq{NodeId: testAdminNodeID, ServiceName: "missing"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_NOT_FOUND, rsp.GetRetInfo().GetCode())
}

func TestServiceImpl_UpdateServiceDeployment_ValidInput_ShouldSucceed(t *testing.T) {
	db := setupSysDeployTestDB(t)
	svc := NewService(&database.Manager{}, testAdminNodeID)
	svc.dao = NewDAO(db)
	require.NoError(t, svc.dao.Create(context.Background(), &Deployment{
		NodeID:      testAdminNodeID,
		ServiceName: "svc_update",
		Host:        "127.0.0.1",
		Port:        18081,
		Status:      "active",
	}))

	rsp, err := svc.UpdateServiceDeployment(context.Background(), &pb.UpdateServiceDeploymentReq{
		NodeId:      testAdminNodeID,
		ServiceName: "svc_update",
		Deployment: &pb.ServiceDeployment{
			NodeId:      testAdminNodeID,
			ServiceName: "svc_update",
			Host:        "127.0.0.2",
			Port:        18082,
			Status:      "active",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Equal(t, "127.0.0.2", rsp.GetDeployment().GetHost())
}

func TestServiceImpl_UpdateDeployment_RecomputesDerivedAddresses(t *testing.T) {
	db := setupSysDeployTestDB(t)
	svc := NewService(&database.Manager{}, testAdminNodeID)
	svc.dao = NewDAO(db)
	ctx := context.Background()

	require.NoError(t, svc.dao.Create(ctx, &Deployment{
		NodeID:      testAdminNodeID,
		ServiceName: "svc_update_address",
		Protocol:    "http",
		Host:        "127.0.0.1",
		Port:        18081,
		GatewayPath: "trpc.moox.test.Service",
		Status:      "active",
	}))

	rsp, err := svc.UpdateServiceDeployment(ctx, &pb.UpdateServiceDeploymentReq{
		NodeId:      testAdminNodeID,
		ServiceName: "svc_update_address",
		Deployment: &pb.ServiceDeployment{
			NodeId:      testAdminNodeID,
			ServiceName: "svc_update_address",
			Protocol:    "http",
			Host:        "127.0.0.2",
			Port:        18082,
			GatewayPath: "trpc.moox.test.Service",
			Status:      "active",
			BaseUrl:     "http://127.0.0.1:18081",
			RpcAddress:  "127.0.0.1:18081",
		},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Equal(t, "http://127.0.0.2:18082", rsp.GetDeployment().GetBaseUrl())
	assert.Equal(t, "127.0.0.2:18082", rsp.GetDeployment().GetRpcAddress())

	detail, ok := svc.ResolveAdminServiceDetail(ctx, testAdminNodeID, "svc_update_address")
	require.True(t, ok)
	assert.Equal(t, "127.0.0.2:18082", detail.Address)
}

func TestDAO_DeleteAndRecreate_CanRepeat(t *testing.T) {
	dao := NewDAO(setupSysDeployTestDB(t))
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		require.NoError(t, dao.Create(ctx, &Deployment{
			NodeID:      testAdminNodeID,
			ServiceName: "repeatable",
			Host:        "127.0.0.1",
			Port:        19090,
			Status:      "active",
		}))
		require.NoError(t, dao.Delete(ctx, testAdminNodeID, "repeatable"))
	}
}

func TestValidateDeployment_RejectsInvalidStaticDirectoryFields(t *testing.T) {
	tests := []struct {
		name    string
		item    *Deployment
		wantErr string
	}{
		{"port zero", &Deployment{NodeID: "node-a", ServiceName: "svc", Protocol: "http", Host: "127.0.0.1", Port: 0}, "port must be between"},
		{"port too large", &Deployment{NodeID: "node-a", ServiceName: "svc", Protocol: "http", Host: "127.0.0.1", Port: 65536}, "port must be between"},
		{"unsupported protocol", &Deployment{NodeID: "node-a", ServiceName: "svc", Protocol: "udp", Host: "127.0.0.1", Port: 80}, "protocol must be"},
		{"unsupported scope", &Deployment{NodeID: "node-a", ServiceName: "svc", Protocol: "http", Host: "127.0.0.1", Port: 80, Scope: "global"}, "scope must be"},
		{"unsupported status", &Deployment{NodeID: "node-a", ServiceName: "svc", Protocol: "http", Host: "127.0.0.1", Port: 80, Status: "healthy"}, "status must be"},
		{"invalid extra JSON", &Deployment{NodeID: "node-a", ServiceName: "svc", Protocol: "http", Host: "127.0.0.1", Port: 80, ExtraConfig: "{"}, "extra_config must be"},
		{"unspecified IP", &Deployment{NodeID: "node-a", ServiceName: "svc", Protocol: "http", Host: "0.0.0.0", Port: 80}, "routable unicast"},
		{"link-local IP", &Deployment{NodeID: "node-a", ServiceName: "svc", Protocol: "http", Host: "169.254.1.1", Port: 80}, "routable unicast"},
		{"multicast IP", &Deployment{NodeID: "node-a", ServiceName: "svc", Protocol: "http", Host: "224.0.0.1", Port: 80}, "routable unicast"},
		{"hostname unsupported", &Deployment{NodeID: "node-a", ServiceName: "svc", Protocol: "http", Host: "service.local", Port: 80}, "host must be an IP address"},
		{"rpc path starts with slash", &Deployment{NodeID: "node-a", ServiceName: "svc", ServiceKind: "service", Protocol: "http", Host: "127.0.0.1", Port: 80, GatewayPath: "/api/service"}, "gateway_path must be a tRPC service path"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.ErrorContains(t, validateDeployment(tt.item), tt.wantErr)
		})
	}
}

func TestValidateDeployment_AllowsGatewayHTTPPath(t *testing.T) {
	assert.NoError(t, validateDeployment(&Deployment{
		NodeID:      testAdminNodeID,
		ServiceName: "admin_gateway",
		ServiceKind: "gateway",
		Protocol:    "http",
		Host:        "127.0.0.1",
		Port:        11000,
		GatewayPath: "/api/admin",
		Scope:       "public",
		Status:      "active",
		ExtraConfig: "{}",
	}))
}
