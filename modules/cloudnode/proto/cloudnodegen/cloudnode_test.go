package cloudnodepb

import (
	"context"
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
	assert.NotEmpty(t, msg.String())
	msg.ProtoMessage()
}

func noopFilter(req interface{}) (filter.ServerChain, error) {
	return filter.ServerChain{filter.NoopServerFilter}, nil
}

func TestProtoMessages_ShouldSupportResetAndString(t *testing.T) {
	exerciseMessage(t, &CloudNode{})
	exerciseMessage(t, &GetNodeListReq{})
	exerciseMessage(t, &GetNodeListRsp{})
	exerciseMessage(t, &UpdateNodeReq{})
	exerciseMessage(t, &UpdateNodeRsp{})
	exerciseMessage(t, &InvokeFunctionReq{})
	exerciseMessage(t, &ScfInvokeResult{})
	exerciseMessage(t, &InvokeFunctionRsp{})
	exerciseMessage(t, &NodeCreateItem{})
	exerciseMessage(t, &BatchCreateNodesReq{})
	exerciseMessage(t, &BatchChangeResult{})
	exerciseMessage(t, &BatchDeleteNodesReq{})
	exerciseMessage(t, &NodeDeployItem{})
	exerciseMessage(t, &BatchDeployNodesReq{})
	exerciseMessage(t, &CloudAccountSummary{})
	exerciseMessage(t, &CloudAccountInput{})
	exerciseMessage(t, &ListCloudAccountsReq{})
	exerciseMessage(t, &ListCloudAccountsRsp{})
	exerciseMessage(t, &CreateCloudAccountReq{})
	exerciseMessage(t, &CreateCloudAccountRsp{})
	exerciseMessage(t, &UpdateCloudAccountReq{})
	exerciseMessage(t, &UpdateCloudAccountRsp{})
	exerciseMessage(t, &DeleteCloudAccountReq{})
	exerciseMessage(t, &DeleteCloudAccountRsp{})
	exerciseMessage(t, &CloudAccountSecret{})
	exerciseMessage(t, &GetCOSAccountInfoReq{})
	exerciseMessage(t, &GetCOSAccountInfoRsp{})
	exerciseMessage(t, &CloudRegion{})
	exerciseMessage(t, &ListCloudRegionsReq{})
	exerciseMessage(t, &ListCloudRegionsRsp{})
	exerciseMessage(t, &PackageListItem{})
	exerciseMessage(t, &GetPackageListReq{})
	exerciseMessage(t, &GetPackageListRsp{})
	exerciseMessage(t, &PackageDetail{})
	exerciseMessage(t, &GetPackageDetailReq{})
	exerciseMessage(t, &GetPackageDetailRsp{})
	exerciseMessage(t, &DeletePackageReq{})
	exerciseMessage(t, &DeletePackageRsp{})
	exerciseMessage(t, &PackageDownloadURL{})
	exerciseMessage(t, &GetPackageDownloadURLReq{})
	exerciseMessage(t, &GetPackageDownloadURLRsp{})
	exerciseMessage(t, &InitPackageUploadReq{})
	exerciseMessage(t, &InitPackageUploadRsp{})
	exerciseMessage(t, &CompletePackageUploadReq{})
	exerciseMessage(t, &CompletePackageUploadRsp{})
	exerciseMessage(t, &LocalDNSReportItem{})
	exerciseMessage(t, &ReportHeartbeatReq{})
	exerciseMessage(t, &ControlDirective{})
	exerciseMessage(t, &ReportHeartbeatRsp{})
	exerciseMessage(t, &JobItem{})
	exerciseMessage(t, &SubmitJobItemsReq{})
	exerciseMessage(t, &JobItemAck{})
	exerciseMessage(t, &SubmitJobItemsRsp{})
	exerciseMessage(t, &PollJobItemsReq{})
	exerciseMessage(t, &PolledJobItem{})
	exerciseMessage(t, &PollJobItemsRsp{})
	exerciseMessage(t, &ReportJobItemStatusReq{})
	exerciseMessage(t, &ReportJobItemStatusRsp{})
	exerciseMessage(t, &CancelJobItemReq{})
	exerciseMessage(t, &CancelJobItemRsp{})
	exerciseMessage(t, &GetJobItemReq{})
	exerciseMessage(t, &JobItemDetail{})
	exerciseMessage(t, &GetJobItemRsp{})
	exerciseMessage(t, &ListJobItemsReq{})
	exerciseMessage(t, &ListJobItemsRsp{})
	exerciseMessage(t, &JobItemAttempt{})
	exerciseMessage(t, &ListJobItemAttemptsReq{})
	exerciseMessage(t, &ListJobItemAttemptsRsp{})
	exerciseMessage(t, &InvokeSyncPayload{})
	exerciseMessage(t, &InvokeSyncReq{})
	exerciseMessage(t, &InvokeSyncResult{})
	exerciseMessage(t, &InvokeSyncRsp{})
}

func TestNilGetters_ShouldReturnZeroValues(t *testing.T) {
	var nilCloudNode *CloudNode
	_ = nilCloudNode
	var nilGetNodeListReq *GetNodeListReq
	_ = nilGetNodeListReq
	var nilGetNodeListRsp *GetNodeListRsp
	_ = nilGetNodeListRsp
	var nilUpdateNodeReq *UpdateNodeReq
	_ = nilUpdateNodeReq
	var nilUpdateNodeRsp *UpdateNodeRsp
	_ = nilUpdateNodeRsp
}

type CloudNodeMgrServiceStub struct{}
func (s *CloudNodeMgrServiceStub) GetNodeList(context.Context, *GetNodeListReq) (*GetNodeListRsp, error) {
	return &GetNodeListRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) UpdateNode(context.Context, *UpdateNodeReq) (*UpdateNodeRsp, error) {
	return &UpdateNodeRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) InvokeFunction(context.Context, *InvokeFunctionReq) (*InvokeFunctionRsp, error) {
	return &InvokeFunctionRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) BatchCreateNodes(context.Context, *BatchCreateNodesReq) (*BatchChangeResult, error) {
	return &BatchChangeResult{}, nil
}
func (s *CloudNodeMgrServiceStub) BatchDeleteNodes(context.Context, *BatchDeleteNodesReq) (*BatchChangeResult, error) {
	return &BatchChangeResult{}, nil
}
func (s *CloudNodeMgrServiceStub) BatchDeployNodes(context.Context, *BatchDeployNodesReq) (*BatchChangeResult, error) {
	return &BatchChangeResult{}, nil
}
func (s *CloudNodeMgrServiceStub) ListCloudAccounts(context.Context, *ListCloudAccountsReq) (*ListCloudAccountsRsp, error) {
	return &ListCloudAccountsRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) CreateCloudAccount(context.Context, *CreateCloudAccountReq) (*CreateCloudAccountRsp, error) {
	return &CreateCloudAccountRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) UpdateCloudAccount(context.Context, *UpdateCloudAccountReq) (*UpdateCloudAccountRsp, error) {
	return &UpdateCloudAccountRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) DeleteCloudAccount(context.Context, *DeleteCloudAccountReq) (*DeleteCloudAccountRsp, error) {
	return &DeleteCloudAccountRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) GetCOSAccountInfo(context.Context, *GetCOSAccountInfoReq) (*GetCOSAccountInfoRsp, error) {
	return &GetCOSAccountInfoRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) ListCloudRegions(context.Context, *ListCloudRegionsReq) (*ListCloudRegionsRsp, error) {
	return &ListCloudRegionsRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) GetPackageList(context.Context, *GetPackageListReq) (*GetPackageListRsp, error) {
	return &GetPackageListRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) GetPackageDetail(context.Context, *GetPackageDetailReq) (*GetPackageDetailRsp, error) {
	return &GetPackageDetailRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) DeletePackage(context.Context, *DeletePackageReq) (*DeletePackageRsp, error) {
	return &DeletePackageRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) GetPackageDownloadURL(context.Context, *GetPackageDownloadURLReq) (*GetPackageDownloadURLRsp, error) {
	return &GetPackageDownloadURLRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) InitPackageUpload(context.Context, *InitPackageUploadReq) (*InitPackageUploadRsp, error) {
	return &InitPackageUploadRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) CompletePackageUpload(context.Context, *CompletePackageUploadReq) (*CompletePackageUploadRsp, error) {
	return &CompletePackageUploadRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) ReportHeartbeat(context.Context, *ReportHeartbeatReq) (*ReportHeartbeatRsp, error) {
	return &ReportHeartbeatRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) SubmitJobItems(context.Context, *SubmitJobItemsReq) (*SubmitJobItemsRsp, error) {
	return &SubmitJobItemsRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) PollJobItems(context.Context, *PollJobItemsReq) (*PollJobItemsRsp, error) {
	return &PollJobItemsRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) ReportJobItemStatus(context.Context, *ReportJobItemStatusReq) (*ReportJobItemStatusRsp, error) {
	return &ReportJobItemStatusRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) CancelJobItem(context.Context, *CancelJobItemReq) (*CancelJobItemRsp, error) {
	return &CancelJobItemRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) GetJobItem(context.Context, *GetJobItemReq) (*GetJobItemRsp, error) {
	return &GetJobItemRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) ListJobItems(context.Context, *ListJobItemsReq) (*ListJobItemsRsp, error) {
	return &ListJobItemsRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) ListJobItemAttempts(context.Context, *ListJobItemAttemptsReq) (*ListJobItemAttemptsRsp, error) {
	return &ListJobItemAttemptsRsp{}, nil
}
func (s *CloudNodeMgrServiceStub) InvokeSync(context.Context, *InvokeSyncReq) (*InvokeSyncRsp, error) {
	return &InvokeSyncRsp{}, nil
}

func TestCloudNodeMgrServiceHandlers_ShouldDispatchRPCs(t *testing.T) {
	stub := &CloudNodeMgrServiceStub{}
	ctx := context.Background()
	rsp, err := CloudNodeMgrService_GetNodeList_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &GetNodeListRsp{}, rsp)
	rsp, err = CloudNodeMgrService_UpdateNode_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &UpdateNodeRsp{}, rsp)
	rsp, err = CloudNodeMgrService_InvokeFunction_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &InvokeFunctionRsp{}, rsp)
	rsp, err = CloudNodeMgrService_BatchCreateNodes_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &BatchChangeResult{}, rsp)
	rsp, err = CloudNodeMgrService_BatchDeleteNodes_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &BatchChangeResult{}, rsp)
	rsp, err = CloudNodeMgrService_BatchDeployNodes_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &BatchChangeResult{}, rsp)
	rsp, err = CloudNodeMgrService_ListCloudAccounts_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &ListCloudAccountsRsp{}, rsp)
	rsp, err = CloudNodeMgrService_CreateCloudAccount_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &CreateCloudAccountRsp{}, rsp)
	rsp, err = CloudNodeMgrService_UpdateCloudAccount_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &UpdateCloudAccountRsp{}, rsp)
	rsp, err = CloudNodeMgrService_DeleteCloudAccount_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &DeleteCloudAccountRsp{}, rsp)
	rsp, err = CloudNodeMgrService_GetCOSAccountInfo_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &GetCOSAccountInfoRsp{}, rsp)
	rsp, err = CloudNodeMgrService_ListCloudRegions_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &ListCloudRegionsRsp{}, rsp)
	rsp, err = CloudNodeMgrService_GetPackageList_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &GetPackageListRsp{}, rsp)
	rsp, err = CloudNodeMgrService_GetPackageDetail_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &GetPackageDetailRsp{}, rsp)
	rsp, err = CloudNodeMgrService_DeletePackage_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &DeletePackageRsp{}, rsp)
	rsp, err = CloudNodeMgrService_GetPackageDownloadURL_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &GetPackageDownloadURLRsp{}, rsp)
	rsp, err = CloudNodeMgrService_InitPackageUpload_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &InitPackageUploadRsp{}, rsp)
	rsp, err = CloudNodeMgrService_CompletePackageUpload_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &CompletePackageUploadRsp{}, rsp)
	rsp, err = CloudNodeMgrService_ReportHeartbeat_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &ReportHeartbeatRsp{}, rsp)
	rsp, err = CloudNodeMgrService_SubmitJobItems_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &SubmitJobItemsRsp{}, rsp)
	rsp, err = CloudNodeMgrService_PollJobItems_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &PollJobItemsRsp{}, rsp)
	rsp, err = CloudNodeMgrService_ReportJobItemStatus_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &ReportJobItemStatusRsp{}, rsp)
	rsp, err = CloudNodeMgrService_CancelJobItem_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &CancelJobItemRsp{}, rsp)
	rsp, err = CloudNodeMgrService_GetJobItem_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &GetJobItemRsp{}, rsp)
	rsp, err = CloudNodeMgrService_ListJobItems_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &ListJobItemsRsp{}, rsp)
	rsp, err = CloudNodeMgrService_ListJobItemAttempts_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &ListJobItemAttemptsRsp{}, rsp)
	rsp, err = CloudNodeMgrService_InvokeSync_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &InvokeSyncRsp{}, rsp)
}

func TestUnimplementedCloudNodeMgr_ShouldReturnErrors(t *testing.T) {
	svc := &UnimplementedCloudNodeMgr{}
	ctx := context.Background()
	_, err := svc.GetNodeList(ctx, &GetNodeListReq{})
	assert.Error(t, err)
	_, err := svc.UpdateNode(ctx, &UpdateNodeReq{})
	assert.Error(t, err)
	_, err := svc.InvokeFunction(ctx, &InvokeFunctionReq{})
	assert.Error(t, err)
	_, err := svc.BatchCreateNodes(ctx, &BatchCreateNodesReq{})
	assert.Error(t, err)
	_, err := svc.BatchDeleteNodes(ctx, &BatchDeleteNodesReq{})
	assert.Error(t, err)
}

func TestRegisterCloudNodeMgrService_ShouldRegisterWithoutPanic(t *testing.T) {
	stub := &CloudNodeMgrServiceStub{}
	s := &fakeTRPCService{}
	require.NotPanics(t, func() {
		RegisterCloudNodeMgrService(s, stub)
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
