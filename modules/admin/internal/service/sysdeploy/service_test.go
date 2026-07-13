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
