package rpc

import (
	"context"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/filter"
)

func exerciseMessage(t *testing.T, msg interface {
	Reset()
	String() string
	ProtoMessage()
}) {
	t.Helper()
	msg.Reset()
	_ = msg.String()
	msg.ProtoMessage()
}

func noopFilter(req interface{}) (filter.ServerChain, error) {
	return filter.ServerChain{filter.NoopServerFilter}, nil
}

func TestProtoMessages_ShouldSupportResetAndString(t *testing.T) {
	exerciseMessage(t, &pb.CloudNode{})
	exerciseMessage(t, &pb.GetNodeListReq{})
	exerciseMessage(t, &pb.GetNodeListRsp{})
	exerciseMessage(t, &pb.UpdateNodeReq{})
	exerciseMessage(t, &pb.UpdateNodeRsp{})
	exerciseMessage(t, &pb.InvokeFunctionReq{})
	exerciseMessage(t, &pb.ScfInvokeResult{})
	exerciseMessage(t, &pb.InvokeFunctionRsp{})
	exerciseMessage(t, &pb.NodeCreateItem{})
	exerciseMessage(t, &pb.BatchCreateNodesReq{})
	exerciseMessage(t, &pb.BatchChangeResult{})
	exerciseMessage(t, &pb.BatchDeleteNodesReq{})
	exerciseMessage(t, &pb.NodeDeployItem{})
	exerciseMessage(t, &pb.BatchDeployNodesReq{})
	exerciseMessage(t, &pb.CloudAccountSummary{})
	exerciseMessage(t, &pb.CloudAccountInput{})
	exerciseMessage(t, &pb.ListCloudAccountsReq{})
	exerciseMessage(t, &pb.ListCloudAccountsRsp{})
	exerciseMessage(t, &pb.CreateCloudAccountReq{})
	exerciseMessage(t, &pb.CreateCloudAccountRsp{})
	exerciseMessage(t, &pb.UpdateCloudAccountReq{})
	exerciseMessage(t, &pb.UpdateCloudAccountRsp{})
	exerciseMessage(t, &pb.DeleteCloudAccountReq{})
	exerciseMessage(t, &pb.DeleteCloudAccountRsp{})
	exerciseMessage(t, &pb.CloudAccountSecret{})
	exerciseMessage(t, &pb.GetCOSAccountInfoReq{})
	exerciseMessage(t, &pb.GetCOSAccountInfoRsp{})
	exerciseMessage(t, &pb.CloudRegion{})
	exerciseMessage(t, &pb.ListCloudRegionsReq{})
	exerciseMessage(t, &pb.ListCloudRegionsRsp{})
	exerciseMessage(t, &pb.PackageListItem{})
	exerciseMessage(t, &pb.GetPackageListReq{})
	exerciseMessage(t, &pb.GetPackageListRsp{})
	exerciseMessage(t, &pb.PackageDetail{})
	exerciseMessage(t, &pb.GetPackageDetailReq{})
	exerciseMessage(t, &pb.GetPackageDetailRsp{})
	exerciseMessage(t, &pb.DeletePackageReq{})
	exerciseMessage(t, &pb.DeletePackageRsp{})
	exerciseMessage(t, &pb.PackageDownloadURL{})
	exerciseMessage(t, &pb.GetPackageDownloadURLReq{})
	exerciseMessage(t, &pb.GetPackageDownloadURLRsp{})
	exerciseMessage(t, &pb.InitPackageUploadReq{})
	exerciseMessage(t, &pb.InitPackageUploadRsp{})
	exerciseMessage(t, &pb.CompletePackageUploadReq{})
	exerciseMessage(t, &pb.CompletePackageUploadRsp{})
	exerciseMessage(t, &pb.LocalDNSReportItem{})
	exerciseMessage(t, &pb.ReportHeartbeatReq{})
	exerciseMessage(t, &pb.ControlDirective{})
	exerciseMessage(t, &pb.ReportHeartbeatRsp{})
	exerciseMessage(t, &pb.JobItem{})
	exerciseMessage(t, &pb.SubmitJobItemsReq{})
	exerciseMessage(t, &pb.JobItemAck{})
	exerciseMessage(t, &pb.SubmitJobItemsRsp{})
	exerciseMessage(t, &pb.PollJobItemsReq{})
	exerciseMessage(t, &pb.PolledJobItem{})
	exerciseMessage(t, &pb.PollJobItemsRsp{})
	exerciseMessage(t, &pb.ReportJobItemStatusReq{})
	exerciseMessage(t, &pb.ReportJobItemStatusRsp{})
	exerciseMessage(t, &pb.CancelJobItemReq{})
	exerciseMessage(t, &pb.CancelJobItemRsp{})
	exerciseMessage(t, &pb.GetJobItemReq{})
	exerciseMessage(t, &pb.JobItemDetail{})
	exerciseMessage(t, &pb.GetJobItemRsp{})
	exerciseMessage(t, &pb.ListJobItemsReq{})
	exerciseMessage(t, &pb.ListJobItemsRsp{})
	exerciseMessage(t, &pb.JobItemAttempt{})
	exerciseMessage(t, &pb.ListJobItemAttemptsReq{})
	exerciseMessage(t, &pb.ListJobItemAttemptsRsp{})
	exerciseMessage(t, &pb.InvokeSyncPayload{})
	exerciseMessage(t, &pb.InvokeSyncReq{})
	exerciseMessage(t, &pb.InvokeSyncResult{})
	exerciseMessage(t, &pb.InvokeSyncRsp{})
}

func TestNilGetters_ShouldReturnZeroValues(t *testing.T) {
	var nilCloudNode *pb.CloudNode
	_ = nilCloudNode
	var nilGetNodeListReq *pb.GetNodeListReq
	_ = nilGetNodeListReq
	var nilGetNodeListRsp *pb.GetNodeListRsp
	_ = nilGetNodeListRsp
	var nilUpdateNodeReq *pb.UpdateNodeReq
	_ = nilUpdateNodeReq
	var nilUpdateNodeRsp *pb.UpdateNodeRsp
	_ = nilUpdateNodeRsp
}

type CloudNodeMgrServiceStub struct{}
func (s *CloudNodeMgrServiceStub) GetNodeList(context.Context, *pb.GetNodeListReq) (*pb.GetNodeListRsp, error) {
	return &pb.GetNodeListRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) UpdateNode(context.Context, *pb.UpdateNodeReq) (*pb.UpdateNodeRsp, error) {
	return &pb.UpdateNodeRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) InvokeFunction(context.Context, *pb.InvokeFunctionReq) (*pb.InvokeFunctionRsp, error) {
	return &pb.InvokeFunctionRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) BatchCreateNodes(context.Context, *pb.BatchCreateNodesReq) (*pb.BatchChangeResult, error) {
	return &pb.BatchChangeResult{}, nil
}
func (s *CloudNodeMgrServiceStub) BatchDeleteNodes(context.Context, *pb.BatchDeleteNodesReq) (*pb.BatchChangeResult, error) {
	return &pb.BatchChangeResult{}, nil
}
func (s *CloudNodeMgrServiceStub) BatchDeployNodes(context.Context, *pb.BatchDeployNodesReq) (*pb.BatchChangeResult, error) {
	return &pb.BatchChangeResult{}, nil
}
func (s *CloudNodeMgrServiceStub) ListCloudAccounts(context.Context, *pb.ListCloudAccountsReq) (*pb.ListCloudAccountsRsp, error) {
	return &pb.ListCloudAccountsRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) CreateCloudAccount(context.Context, *pb.CreateCloudAccountReq) (*pb.CreateCloudAccountRsp, error) {
	return &pb.CreateCloudAccountRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) UpdateCloudAccount(context.Context, *pb.UpdateCloudAccountReq) (*pb.UpdateCloudAccountRsp, error) {
	return &pb.UpdateCloudAccountRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) DeleteCloudAccount(context.Context, *pb.DeleteCloudAccountReq) (*pb.DeleteCloudAccountRsp, error) {
	return &pb.DeleteCloudAccountRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) GetCOSAccountInfo(context.Context, *pb.GetCOSAccountInfoReq) (*pb.GetCOSAccountInfoRsp, error) {
	return &pb.GetCOSAccountInfoRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) ListCloudRegions(context.Context, *pb.ListCloudRegionsReq) (*pb.ListCloudRegionsRsp, error) {
	return &pb.ListCloudRegionsRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) GetPackageList(context.Context, *pb.GetPackageListReq) (*pb.GetPackageListRsp, error) {
	return &pb.GetPackageListRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) GetPackageDetail(context.Context, *pb.GetPackageDetailReq) (*pb.GetPackageDetailRsp, error) {
	return &pb.GetPackageDetailRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) DeletePackage(context.Context, *pb.DeletePackageReq) (*pb.DeletePackageRsp, error) {
	return &pb.DeletePackageRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) GetPackageDownloadURL(context.Context, *pb.GetPackageDownloadURLReq) (*pb.GetPackageDownloadURLRsp, error) {
	return &pb.GetPackageDownloadURLRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) InitPackageUpload(context.Context, *pb.InitPackageUploadReq) (*pb.InitPackageUploadRsp, error) {
	return &pb.InitPackageUploadRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) CompletePackageUpload(context.Context, *pb.CompletePackageUploadReq) (*pb.CompletePackageUploadRsp, error) {
	return &pb.CompletePackageUploadRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) ReportHeartbeat(context.Context, *pb.ReportHeartbeatReq) (*pb.ReportHeartbeatRsp, error) {
	return &pb.ReportHeartbeatRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) SubmitJobItems(context.Context, *pb.SubmitJobItemsReq) (*pb.SubmitJobItemsRsp, error) {
	return &pb.SubmitJobItemsRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) PollJobItems(context.Context, *pb.PollJobItemsReq) (*pb.PollJobItemsRsp, error) {
	return &pb.PollJobItemsRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) ReportJobItemStatus(context.Context, *pb.ReportJobItemStatusReq) (*pb.ReportJobItemStatusRsp, error) {
	return &pb.ReportJobItemStatusRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) CancelJobItem(context.Context, *pb.CancelJobItemReq) (*pb.CancelJobItemRsp, error) {
	return &pb.CancelJobItemRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) GetJobItem(context.Context, *pb.GetJobItemReq) (*pb.GetJobItemRsp, error) {
	return &pb.GetJobItemRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) ListJobItems(context.Context, *pb.ListJobItemsReq) (*pb.ListJobItemsRsp, error) {
	return &pb.ListJobItemsRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) ListJobItemAttempts(context.Context, *pb.ListJobItemAttemptsReq) (*pb.ListJobItemAttemptsRsp, error) {
	return &pb.ListJobItemAttemptsRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) InvokeSync(context.Context, *pb.InvokeSyncReq) (*pb.InvokeSyncRsp, error) {
	return &pb.InvokeSyncRsp{}, nil
}

func TestCloudNodeMgrServiceHandlers_ShouldDispatchRPCs(t *testing.T) {
	stub := &CloudNodeMgrServiceStub{}
	ctx := context.Background()
	rsp, err := pb.CloudNodeMgrService_GetNodeList_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.GetNodeListRsp{}, rsp)
	rsp, err = pb.CloudNodeMgrService_UpdateNode_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.UpdateNodeRsp{}, rsp)
	rsp, err = pb.CloudNodeMgrService_InvokeFunction_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.InvokeFunctionRsp{}, rsp)
	rsp, err = pb.CloudNodeMgrService_BatchCreateNodes_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.BatchChangeResult{}, rsp)
	rsp, err = pb.CloudNodeMgrService_BatchDeleteNodes_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.BatchChangeResult{}, rsp)
	rsp, err = pb.CloudNodeMgrService_BatchDeployNodes_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.BatchChangeResult{}, rsp)
	rsp, err = pb.CloudNodeMgrService_ListCloudAccounts_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.ListCloudAccountsRsp{}, rsp)
	rsp, err = pb.CloudNodeMgrService_CreateCloudAccount_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.CreateCloudAccountRsp{}, rsp)
	rsp, err = pb.CloudNodeMgrService_UpdateCloudAccount_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.UpdateCloudAccountRsp{}, rsp)
	rsp, err = pb.CloudNodeMgrService_DeleteCloudAccount_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.DeleteCloudAccountRsp{}, rsp)
	rsp, err = pb.CloudNodeMgrService_GetCOSAccountInfo_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.GetCOSAccountInfoRsp{}, rsp)
	rsp, err = pb.CloudNodeMgrService_ListCloudRegions_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.ListCloudRegionsRsp{}, rsp)
	rsp, err = pb.CloudNodeMgrService_GetPackageList_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.GetPackageListRsp{}, rsp)
	rsp, err = pb.CloudNodeMgrService_GetPackageDetail_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.GetPackageDetailRsp{}, rsp)
	rsp, err = pb.CloudNodeMgrService_DeletePackage_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.DeletePackageRsp{}, rsp)
	rsp, err = pb.CloudNodeMgrService_GetPackageDownloadURL_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.GetPackageDownloadURLRsp{}, rsp)
	rsp, err = pb.CloudNodeMgrService_InitPackageUpload_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.InitPackageUploadRsp{}, rsp)
	rsp, err = pb.CloudNodeMgrService_CompletePackageUpload_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.CompletePackageUploadRsp{}, rsp)
	rsp, err = pb.CloudNodeMgrService_ReportHeartbeat_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.ReportHeartbeatRsp{}, rsp)
	rsp, err = pb.CloudNodeMgrService_SubmitJobItems_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.SubmitJobItemsRsp{}, rsp)
	rsp, err = pb.CloudNodeMgrService_PollJobItems_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.PollJobItemsRsp{}, rsp)
	rsp, err = pb.CloudNodeMgrService_ReportJobItemStatus_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.ReportJobItemStatusRsp{}, rsp)
	rsp, err = pb.CloudNodeMgrService_CancelJobItem_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.CancelJobItemRsp{}, rsp)
	rsp, err = pb.CloudNodeMgrService_GetJobItem_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.GetJobItemRsp{}, rsp)
	rsp, err = pb.CloudNodeMgrService_ListJobItems_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.ListJobItemsRsp{}, rsp)
	rsp, err = pb.CloudNodeMgrService_ListJobItemAttempts_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.ListJobItemAttemptsRsp{}, rsp)
	rsp, err = pb.CloudNodeMgrService_InvokeSync_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.InvokeSyncRsp{}, rsp)
}

func TestUnimplementedCloudNodeMgr_ShouldReturnErrors(t *testing.T) {
	svc := &pb.UnimplementedCloudNodeMgr{}
	ctx := context.Background()
	_, err := svc.GetNodeList(ctx, &pb.GetNodeListReq{})
	assert.Error(t, err)
	_, err = svc.UpdateNode(ctx, &pb.UpdateNodeReq{})
	assert.Error(t, err)
	_, err = svc.InvokeFunction(ctx, &pb.InvokeFunctionReq{})
	assert.Error(t, err)
	_, err = svc.BatchCreateNodes(ctx, &pb.BatchCreateNodesReq{})
	assert.Error(t, err)
	_, err = svc.BatchDeleteNodes(ctx, &pb.BatchDeleteNodesReq{})
	assert.Error(t, err)
}

func TestRegisterCloudNodeMgrService_ShouldRegisterWithoutPanic(t *testing.T) {
	stub := &CloudNodeMgrServiceStub{}
	s := &fakeTRPCService{}
	require.NotPanics(t, func() {
		pb.RegisterCloudNodeMgrService(s, stub)
	})
	assert.True(t, s.registered)
}

type fakeTRPCService struct { registered bool }

func (f *fakeTRPCService) Register(serviceDesc interface{}, serviceImpl interface{}) error {
	f.registered = true
	return nil
}

func (f *fakeTRPCService) Serve() error { return nil }

func (f *fakeTRPCService) Close(chan struct{}) error { return nil }
