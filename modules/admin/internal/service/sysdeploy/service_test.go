package sysdeploy

import (
	"context"
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

func setupSysDeployTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(schema.AdminSQL()).Error)
	return db
}

func TestServiceImpl_SeedDefaults_ShouldInsertRows(t *testing.T) {
	db := setupSysDeployTestDB(t)
	svc := NewService(&database.Manager{})
	svc.dao = NewDAO(db)

	require.NoError(t, svc.SeedDefaults(context.Background()))

	rows, total, err := svc.dao.List(context.Background(), ListFilter{}, 0, 100)
	require.NoError(t, err)
	assert.Greater(t, total, int64(0))
	assert.NotEmpty(t, rows)
}

func TestServiceImpl_CreateServiceDeployment_InvalidParam_ShouldReturnError(t *testing.T) {
	db := setupSysDeployTestDB(t)
	svc := NewService(&database.Manager{})
	svc.dao = NewDAO(db)

	rsp, err := svc.CreateServiceDeployment(context.Background(), &pb.CreateServiceDeploymentReq{
		Deployment: &pb.ServiceDeployment{ServiceName: "svc-a"},
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestServiceImpl_CreateAndGetServiceDeployment_ValidInput_ShouldSucceed(t *testing.T) {
	db := setupSysDeployTestDB(t)
	svc := NewService(&database.Manager{})
	svc.dao = NewDAO(db)

	createRsp, err := svc.CreateServiceDeployment(context.Background(), &pb.CreateServiceDeploymentReq{
		Deployment: &pb.ServiceDeployment{
			ServiceName: "test_svc",
			Host:        "127.0.0.1",
			Port:        18080,
			GatewayPath: "trpc.moox.test.Service",
			Status:      "active",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, createRsp.GetRetInfo().GetCode())

	getRsp, err := svc.GetServiceDeployment(context.Background(), &pb.GetServiceDeploymentReq{ServiceName: "test_svc"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, getRsp.GetRetInfo().GetCode())
	assert.Equal(t, "test_svc", getRsp.GetDeployment().GetServiceName())
}

func TestServiceImpl_DeleteServiceDeployment_MissingName_ShouldReturnInvalidParam(t *testing.T) {
	db := setupSysDeployTestDB(t)
	svc := NewService(&database.Manager{})
	svc.dao = NewDAO(db)

	rsp, err := svc.DeleteServiceDeployment(context.Background(), &pb.DeleteServiceDeploymentReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestServiceImpl_ResolveGatewayServiceDetail_ActiveTrade_ShouldResolve(t *testing.T) {
	db := setupSysDeployTestDB(t)
	svc := NewService(&database.Manager{})
	svc.dao = NewDAO(db)
	require.NoError(t, svc.dao.Create(context.Background(), &Deployment{
		ServiceName: "moox_trade",
		Host:        "127.0.0.1",
		Port:        11210,
		GatewayPath: "trpc.moox.trade.Trade",
		Status:      "active",
	}))

	detail, ok := svc.ResolveGatewayServiceDetail(context.Background(), "trade")
	assert.True(t, ok)
	assert.Equal(t, "127.0.0.1:11210", detail.Address)
	assert.Equal(t, "trpc.moox.trade.Trade", detail.Path)
}

func TestServiceImpl_ListActiveServiceDeployments_ActiveRows_ShouldReturnEndpoints(t *testing.T) {
	db := setupSysDeployTestDB(t)
	svc := NewService(&database.Manager{})
	svc.dao = NewDAO(db)
	require.NoError(t, svc.dao.Create(context.Background(), &Deployment{
		ServiceName: "svc_active",
		Host:        "127.0.0.1",
		Port:        19191,
		GatewayPath: "trpc.moox.test.Active",
		Status:      "active",
	}))

	rsp, err := svc.ListActiveServiceDeployments(context.Background(), &pb.ListActiveServiceDeploymentsReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Contains(t, rsp.GetDeploymentMap(), "svc_active")
}

func TestEndpointMap_MultipleRows_ShouldIndexByServiceName(t *testing.T) {
	rows := []Deployment{
		{ServiceName: "a", Host: "127.0.0.1", Port: 1},
		{ServiceName: "b", Host: "127.0.0.1", Port: 2},
	}
	m := endpointMap(rows)
	assert.Len(t, m, 2)
	assert.Equal(t, "a", m["a"].ServiceName)
}

func TestServiceImpl_ListServiceDeployments_DefaultPage_ShouldReturnRows(t *testing.T) {
	db := setupSysDeployTestDB(t)
	svc := NewService(&database.Manager{})
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
	svc := NewService(&database.Manager{})
	svc.dao = NewDAO(db)

	rsp, err := svc.GetServiceDeployment(context.Background(), &pb.GetServiceDeploymentReq{ServiceName: "missing"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_NOT_FOUND, rsp.GetRetInfo().GetCode())
}

func TestServiceImpl_UpdateServiceDeployment_ValidInput_ShouldSucceed(t *testing.T) {
	db := setupSysDeployTestDB(t)
	svc := NewService(&database.Manager{})
	svc.dao = NewDAO(db)
	require.NoError(t, svc.dao.Create(context.Background(), &Deployment{
		ServiceName: "svc_update",
		Host:        "127.0.0.1",
		Port:        18081,
		Status:      "active",
	}))

	rsp, err := svc.UpdateServiceDeployment(context.Background(), &pb.UpdateServiceDeploymentReq{
		ServiceName: "svc_update",
		Deployment: &pb.ServiceDeployment{
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
	svc := NewService(&database.Manager{})
	svc.dao = NewDAO(db)
	ctx := context.Background()

	require.NoError(t, svc.dao.Create(ctx, &Deployment{
		ServiceName: "svc_update_address",
		Protocol:    "http",
		Host:        "127.0.0.1",
		Port:        18081,
		GatewayPath: "trpc.moox.test.Service",
		Status:      "active",
	}))

	rsp, err := svc.UpdateServiceDeployment(ctx, &pb.UpdateServiceDeploymentReq{
		ServiceName: "svc_update_address",
		Deployment: &pb.ServiceDeployment{
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

	detail, ok := svc.ResolveGatewayServiceDetail(ctx, "svc_update_address")
	require.True(t, ok)
	assert.Equal(t, "127.0.0.2:18082", detail.Address)
}

func TestDAO_DeleteAndRecreate_CanRepeat(t *testing.T) {
	dao := NewDAO(setupSysDeployTestDB(t))
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		require.NoError(t, dao.Create(ctx, &Deployment{
			ServiceName: "repeatable",
			Host:        "127.0.0.1",
			Port:        19090,
			Status:      "active",
		}))
		require.NoError(t, dao.Delete(ctx, "repeatable"))
	}
}

func TestValidateDeployment_RejectsInvalidStaticDirectoryFields(t *testing.T) {
	tests := []struct {
		name string
		item *Deployment
	}{
		{"port zero", &Deployment{ServiceName: "svc", Protocol: "http", Host: "127.0.0.1", Port: 0}},
		{"port too large", &Deployment{ServiceName: "svc", Protocol: "http", Host: "127.0.0.1", Port: 65536}},
		{"unsupported protocol", &Deployment{ServiceName: "svc", Protocol: "udp", Host: "127.0.0.1", Port: 80}},
		{"unsupported scope", &Deployment{ServiceName: "svc", Protocol: "http", Host: "127.0.0.1", Port: 80, Scope: "global"}},
		{"unsupported status", &Deployment{ServiceName: "svc", Protocol: "http", Host: "127.0.0.1", Port: 80, Status: "healthy"}},
		{"invalid extra JSON", &Deployment{ServiceName: "svc", Protocol: "http", Host: "127.0.0.1", Port: 80, ExtraConfig: "{"}},
		{"unspecified IP", &Deployment{ServiceName: "svc", Protocol: "http", Host: "0.0.0.0", Port: 80}},
		{"link-local IP", &Deployment{ServiceName: "svc", Protocol: "http", Host: "169.254.1.1", Port: 80}},
		{"multicast IP", &Deployment{ServiceName: "svc", Protocol: "http", Host: "224.0.0.1", Port: 80}},
		{"hostname unsupported", &Deployment{ServiceName: "svc", Protocol: "http", Host: "service.local", Port: 80}},
		{"rpc path starts with slash", &Deployment{ServiceName: "svc", ServiceKind: "service", Protocol: "http", Host: "127.0.0.1", Port: 80, GatewayPath: "/api/service"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Error(t, validateDeployment(tt.item))
		})
	}
}

func TestValidateDeployment_AllowsGatewayHTTPPath(t *testing.T) {
	assert.NoError(t, validateDeployment(&Deployment{
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

func TestServiceImpl_GetServiceDeployments_ActiveRows_ShouldReturnMap(t *testing.T) {
	db := setupSysDeployTestDB(t)
	svc := NewService(&database.Manager{})
	svc.dao = NewDAO(db)
	require.NoError(t, svc.dao.Create(context.Background(), &Deployment{
		ServiceName: "svc_map",
		Host:        "127.0.0.1",
		Port:        19090,
		Status:      "active",
	}))

	payload, err := svc.GetServiceDeployments(context.Background())
	require.NoError(t, err)
	assert.Contains(t, payload, "svc_map")
}
