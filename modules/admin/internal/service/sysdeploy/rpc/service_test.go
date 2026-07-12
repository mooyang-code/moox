package rpc

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	mocker "github.com/tencent/goom"
)

type fakeSysDeployService struct {
	sysdeploy.Service
}

func (f *fakeSysDeployService) ListServiceDeployments(ctx context.Context, req *pb.ListServiceDeploymentsReq) (*pb.ListServiceDeploymentsRsp, error) {
	return &pb.ListServiceDeploymentsRsp{RetInfo: retOK()}, nil
}

func (f *fakeSysDeployService) GetServiceDeployment(ctx context.Context, req *pb.GetServiceDeploymentReq) (*pb.GetServiceDeploymentRsp, error) {
	return &pb.GetServiceDeploymentRsp{RetInfo: retOK()}, nil
}

func (f *fakeSysDeployService) CreateServiceDeployment(ctx context.Context, req *pb.CreateServiceDeploymentReq) (*pb.CreateServiceDeploymentRsp, error) {
	return &pb.CreateServiceDeploymentRsp{RetInfo: retOK()}, nil
}

func (f *fakeSysDeployService) UpdateServiceDeployment(ctx context.Context, req *pb.UpdateServiceDeploymentReq) (*pb.UpdateServiceDeploymentRsp, error) {
	return &pb.UpdateServiceDeploymentRsp{RetInfo: retOK()}, nil
}

func (f *fakeSysDeployService) DeleteServiceDeployment(ctx context.Context, req *pb.DeleteServiceDeploymentReq) (*pb.DeleteServiceDeploymentRsp, error) {
	return &pb.DeleteServiceDeploymentRsp{RetInfo: retOK()}, nil
}

func (f *fakeSysDeployService) ListActiveServiceDeployments(ctx context.Context, req *pb.ListActiveServiceDeploymentsReq) (*pb.ListActiveServiceDeploymentsRsp, error) {
	return &pb.ListActiveServiceDeploymentsRsp{RetInfo: retOK()}, nil
}

func retOK() *pb.RetInfo {
	return &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "success"}
}

func TestService_ListServiceDeployments_DelegatesToService(t *testing.T) {
	svc := NewService(&fakeSysDeployService{})
	rsp, err := svc.ListServiceDeployments(context.Background(), &pb.ListServiceDeploymentsReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
}

func TestService_GetServiceDeployment_DelegatesToService(t *testing.T) {
	svc := NewService(&fakeSysDeployService{})
	rsp, err := svc.GetServiceDeployment(context.Background(), &pb.GetServiceDeploymentReq{ServiceName: "moox_trade"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
}

func TestService_CreateServiceDeployment_DelegatesToService(t *testing.T) {
	svc := NewService(&fakeSysDeployService{})
	rsp, err := svc.CreateServiceDeployment(context.Background(), &pb.CreateServiceDeploymentReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
}

func TestService_UpdateServiceDeployment_DelegatesToService(t *testing.T) {
	svc := NewService(&fakeSysDeployService{})
	rsp, err := svc.UpdateServiceDeployment(context.Background(), &pb.UpdateServiceDeploymentReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
}

func TestService_DeleteServiceDeployment_DelegatesToService(t *testing.T) {
	svc := NewService(&fakeSysDeployService{})
	rsp, err := svc.DeleteServiceDeployment(context.Background(), &pb.DeleteServiceDeploymentReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
}

func TestService_ListActiveServiceDeployments_DelegatesToService(t *testing.T) {
	svc := NewService(&fakeSysDeployService{})
	rsp, err := svc.ListActiveServiceDeployments(context.Background(), &pb.ListActiveServiceDeploymentsReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
}

func TestNewService_ShouldWrapUnderlyingService(t *testing.T) {
	inner := &fakeSysDeployService{}
	svc := NewService(inner)
	require.NotNil(t, svc)
	assert.Equal(t, inner, svc.svc)
}

func TestSysDeployService_GoomMock_DelegatesList(t *testing.T) {
	mock := mocker.Create()
	defer mock.Reset()

	svcIface := (sysdeploy.Service)(nil)
	mock.Interface(&svcIface).Method("ListServiceDeployments").Apply(func(_ *mocker.IContext, _ context.Context, _ *pb.ListServiceDeploymentsReq) (*pb.ListServiceDeploymentsRsp, error) {
		return &pb.ListServiceDeploymentsRsp{RetInfo: retOK()}, nil
	})

	rpcSvc := NewService(svcIface)
	rsp, err := rpcSvc.ListServiceDeployments(context.Background(), &pb.ListServiceDeploymentsReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
}
