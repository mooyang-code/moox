package collectorpb

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
	exerciseMessage(t, &TaskRule{})
	exerciseMessage(t, &GetTaskRuleListReq{})
	exerciseMessage(t, &GetTaskRuleListRsp{})
	exerciseMessage(t, &GetTaskRuleDetailReq{})
	exerciseMessage(t, &GetTaskRuleDetailRsp{})
	exerciseMessage(t, &CreateTaskRuleReq{})
	exerciseMessage(t, &CreateTaskRuleRsp{})
	exerciseMessage(t, &UpdateTaskRuleReq{})
	exerciseMessage(t, &UpdateTaskRuleRsp{})
	exerciseMessage(t, &DisableTaskRuleReq{})
	exerciseMessage(t, &DisableTaskRuleRsp{})
	exerciseMessage(t, &TaskInstance{})
	exerciseMessage(t, &TaskInstanceFilter{})
	exerciseMessage(t, &GetTaskInstanceListReq{})
	exerciseMessage(t, &GetTaskInstanceListRsp{})
	exerciseMessage(t, &ReportInstanceStatusReq{})
	exerciseMessage(t, &ReportInstanceStatusRsp{})
	exerciseMessage(t, &DataTypeConfig{})
	exerciseMessage(t, &DataTypeFieldConfig{})
	exerciseMessage(t, &DataTypeConfigDetail{})
	exerciseMessage(t, &GetDataTypeConfigsReq{})
	exerciseMessage(t, &GetDataTypeConfigsRsp{})
	exerciseMessage(t, &GetDataTypeConfigWithFieldsReq{})
	exerciseMessage(t, &GetDataTypeConfigWithFieldsRsp{})
	exerciseMessage(t, &RecalculateAllTaskInstancesReq{})
	exerciseMessage(t, &RecalculateAllTaskInstancesRsp{})
}

func TestNilGetters_ShouldReturnZeroValues(t *testing.T) {
	var nilTaskRule *TaskRule
	_ = nilTaskRule
	var nilGetTaskRuleListReq *GetTaskRuleListReq
	_ = nilGetTaskRuleListReq
	var nilGetTaskRuleListRsp *GetTaskRuleListRsp
	_ = nilGetTaskRuleListRsp
	var nilGetTaskRuleDetailReq *GetTaskRuleDetailReq
	_ = nilGetTaskRuleDetailReq
	var nilGetTaskRuleDetailRsp *GetTaskRuleDetailRsp
	_ = nilGetTaskRuleDetailRsp
}

type CollectMgrServiceStub struct{}
func (s *CollectMgrServiceStub) GetTaskRuleList(context.Context, *GetTaskRuleListReq) (*GetTaskRuleListRsp, error) {
	return &GetTaskRuleListRsp{}, nil
}
func (s *CollectMgrServiceStub) GetTaskRuleDetail(context.Context, *GetTaskRuleDetailReq) (*GetTaskRuleDetailRsp, error) {
	return &GetTaskRuleDetailRsp{}, nil
}
func (s *CollectMgrServiceStub) CreateTaskRule(context.Context, *CreateTaskRuleReq) (*CreateTaskRuleRsp, error) {
	return &CreateTaskRuleRsp{}, nil
}
func (s *CollectMgrServiceStub) UpdateTaskRule(context.Context, *UpdateTaskRuleReq) (*UpdateTaskRuleRsp, error) {
	return &UpdateTaskRuleRsp{}, nil
}
func (s *CollectMgrServiceStub) DisableTaskRule(context.Context, *DisableTaskRuleReq) (*DisableTaskRuleRsp, error) {
	return &DisableTaskRuleRsp{}, nil
}
func (s *CollectMgrServiceStub) GetTaskInstanceList(context.Context, *GetTaskInstanceListReq) (*GetTaskInstanceListRsp, error) {
	return &GetTaskInstanceListRsp{}, nil
}
func (s *CollectMgrServiceStub) ReportTaskStatus(context.Context, *ReportInstanceStatusReq) (*ReportInstanceStatusRsp, error) {
	return &ReportInstanceStatusRsp{}, nil
}
func (s *CollectMgrServiceStub) GetDataTypeConfigs(context.Context, *GetDataTypeConfigsReq) (*GetDataTypeConfigsRsp, error) {
	return &GetDataTypeConfigsRsp{}, nil
}
func (s *CollectMgrServiceStub) GetDataTypeConfigWithFields(context.Context, *GetDataTypeConfigWithFieldsReq) (*GetDataTypeConfigWithFieldsRsp, error) {
	return &GetDataTypeConfigWithFieldsRsp{}, nil
}
func (s *CollectMgrServiceStub) RecalculateAllTaskInstances(context.Context, *RecalculateAllTaskInstancesReq) (*RecalculateAllTaskInstancesRsp, error) {
	return &RecalculateAllTaskInstancesRsp{}, nil
}

func TestCollectMgrServiceHandlers_ShouldDispatchRPCs(t *testing.T) {
	stub := &CollectMgrServiceStub{}
	ctx := context.Background()
	rsp, err := CollectMgrService_GetTaskRuleList_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &GetTaskRuleListRsp{}, rsp)
	rsp, err = CollectMgrService_GetTaskRuleDetail_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &GetTaskRuleDetailRsp{}, rsp)
	rsp, err = CollectMgrService_CreateTaskRule_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &CreateTaskRuleRsp{}, rsp)
	rsp, err = CollectMgrService_UpdateTaskRule_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &UpdateTaskRuleRsp{}, rsp)
	rsp, err = CollectMgrService_DisableTaskRule_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &DisableTaskRuleRsp{}, rsp)
	rsp, err = CollectMgrService_GetTaskInstanceList_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &GetTaskInstanceListRsp{}, rsp)
	rsp, err = CollectMgrService_ReportTaskStatus_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &ReportInstanceStatusRsp{}, rsp)
	rsp, err = CollectMgrService_GetDataTypeConfigs_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &GetDataTypeConfigsRsp{}, rsp)
	rsp, err = CollectMgrService_GetDataTypeConfigWithFields_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &GetDataTypeConfigWithFieldsRsp{}, rsp)
	rsp, err = CollectMgrService_RecalculateAllTaskInstances_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &RecalculateAllTaskInstancesRsp{}, rsp)
}

func TestUnimplementedCollectMgr_ShouldReturnErrors(t *testing.T) {
	svc := &UnimplementedCollectMgr{}
	ctx := context.Background()
	_, err := svc.GetTaskRuleList(ctx, &GetTaskRuleListReq{})
	assert.Error(t, err)
	_, err := svc.GetTaskRuleDetail(ctx, &GetTaskRuleDetailReq{})
	assert.Error(t, err)
	_, err := svc.CreateTaskRule(ctx, &CreateTaskRuleReq{})
	assert.Error(t, err)
	_, err := svc.UpdateTaskRule(ctx, &UpdateTaskRuleReq{})
	assert.Error(t, err)
	_, err := svc.DisableTaskRule(ctx, &DisableTaskRuleReq{})
	assert.Error(t, err)
}

func TestRegisterCollectMgrService_ShouldRegisterWithoutPanic(t *testing.T) {
	stub := &CollectMgrServiceStub{}
	s := &fakeTRPCService{}
	require.NotPanics(t, func() {
		RegisterCollectMgrService(s, stub)
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
