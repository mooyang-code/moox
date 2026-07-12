package rpc

import (
	"context"

	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
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
	exerciseMessage(t, &pb.TaskRule{})
	exerciseMessage(t, &pb.GetTaskRuleListReq{})
	exerciseMessage(t, &pb.GetTaskRuleListRsp{})
	exerciseMessage(t, &pb.GetTaskRuleDetailReq{})
	exerciseMessage(t, &pb.GetTaskRuleDetailRsp{})
	exerciseMessage(t, &pb.CreateTaskRuleReq{})
	exerciseMessage(t, &pb.CreateTaskRuleRsp{})
	exerciseMessage(t, &pb.UpdateTaskRuleReq{})
	exerciseMessage(t, &pb.UpdateTaskRuleRsp{})
	exerciseMessage(t, &pb.DisableTaskRuleReq{})
	exerciseMessage(t, &pb.DisableTaskRuleRsp{})
	exerciseMessage(t, &pb.TaskInstance{})
	exerciseMessage(t, &pb.TaskInstanceFilter{})
	exerciseMessage(t, &pb.GetTaskInstanceListReq{})
	exerciseMessage(t, &pb.GetTaskInstanceListRsp{})
	exerciseMessage(t, &pb.ReportInstanceStatusReq{})
	exerciseMessage(t, &pb.ReportInstanceStatusRsp{})
	exerciseMessage(t, &pb.DataTypeConfig{})
	exerciseMessage(t, &pb.DataTypeFieldConfig{})
	exerciseMessage(t, &pb.DataTypeConfigDetail{})
	exerciseMessage(t, &pb.GetDataTypeConfigsReq{})
	exerciseMessage(t, &pb.GetDataTypeConfigsRsp{})
	exerciseMessage(t, &pb.GetDataTypeConfigWithFieldsReq{})
	exerciseMessage(t, &pb.GetDataTypeConfigWithFieldsRsp{})
	exerciseMessage(t, &pb.RecalculateAllTaskInstancesReq{})
	exerciseMessage(t, &pb.RecalculateAllTaskInstancesRsp{})
}

func TestNilGetters_ShouldReturnZeroValues(t *testing.T) {
	var nilTaskRule *pb.TaskRule
	_ = nilTaskRule
	var nilGetTaskRuleListReq *pb.GetTaskRuleListReq
	_ = nilGetTaskRuleListReq
	var nilGetTaskRuleListRsp *pb.GetTaskRuleListRsp
	_ = nilGetTaskRuleListRsp
	var nilGetTaskRuleDetailReq *pb.GetTaskRuleDetailReq
	_ = nilGetTaskRuleDetailReq
	var nilGetTaskRuleDetailRsp *pb.GetTaskRuleDetailRsp
	_ = nilGetTaskRuleDetailRsp
}

type CollectMgrServiceStub struct{}
func (s *CollectMgrServiceStub) GetTaskRuleList(context.Context, *pb.GetTaskRuleListReq) (*pb.GetTaskRuleListRsp, error) {
	return &pb.GetTaskRuleListRsp{}, nil
}
func (s *CollectMgrServiceStub) GetTaskRuleDetail(context.Context, *pb.GetTaskRuleDetailReq) (*pb.GetTaskRuleDetailRsp, error) {
	return &pb.GetTaskRuleDetailRsp{}, nil
}
func (s *CollectMgrServiceStub) CreateTaskRule(context.Context, *pb.CreateTaskRuleReq) (*pb.CreateTaskRuleRsp, error) {
	return &pb.CreateTaskRuleRsp{}, nil
}
func (s *CollectMgrServiceStub) UpdateTaskRule(context.Context, *pb.UpdateTaskRuleReq) (*pb.UpdateTaskRuleRsp, error) {
	return &pb.UpdateTaskRuleRsp{}, nil
}
func (s *CollectMgrServiceStub) DisableTaskRule(context.Context, *pb.DisableTaskRuleReq) (*pb.DisableTaskRuleRsp, error) {
	return &pb.DisableTaskRuleRsp{}, nil
}
func (s *CollectMgrServiceStub) GetTaskInstanceList(context.Context, *pb.GetTaskInstanceListReq) (*pb.GetTaskInstanceListRsp, error) {
	return &pb.GetTaskInstanceListRsp{}, nil
}
func (s *CollectMgrServiceStub) ReportTaskStatus(context.Context, *pb.ReportInstanceStatusReq) (*pb.ReportInstanceStatusRsp, error) {
	return &pb.ReportInstanceStatusRsp{}, nil
}
func (s *CollectMgrServiceStub) GetDataTypeConfigs(context.Context, *pb.GetDataTypeConfigsReq) (*pb.GetDataTypeConfigsRsp, error) {
	return &pb.GetDataTypeConfigsRsp{}, nil
}
func (s *CollectMgrServiceStub) GetDataTypeConfigWithFields(context.Context, *pb.GetDataTypeConfigWithFieldsReq) (*pb.GetDataTypeConfigWithFieldsRsp, error) {
	return &pb.GetDataTypeConfigWithFieldsRsp{}, nil
}
func (s *CollectMgrServiceStub) RecalculateAllTaskInstances(context.Context, *pb.RecalculateAllTaskInstancesReq) (*pb.RecalculateAllTaskInstancesRsp, error) {
	return &pb.RecalculateAllTaskInstancesRsp{}, nil
}

func TestCollectMgrServiceHandlers_ShouldDispatchRPCs(t *testing.T) {
	stub := &CollectMgrServiceStub{}
	ctx := context.Background()
	rsp, err := pb.CollectMgrService_GetTaskRuleList_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.GetTaskRuleListRsp{}, rsp)
	rsp, err = pb.CollectMgrService_GetTaskRuleDetail_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.GetTaskRuleDetailRsp{}, rsp)
	rsp, err = pb.CollectMgrService_CreateTaskRule_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.CreateTaskRuleRsp{}, rsp)
	rsp, err = pb.CollectMgrService_UpdateTaskRule_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.UpdateTaskRuleRsp{}, rsp)
	rsp, err = pb.CollectMgrService_DisableTaskRule_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.DisableTaskRuleRsp{}, rsp)
	rsp, err = pb.CollectMgrService_GetTaskInstanceList_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.GetTaskInstanceListRsp{}, rsp)
	rsp, err = pb.CollectMgrService_ReportTaskStatus_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.ReportInstanceStatusRsp{}, rsp)
	rsp, err = pb.CollectMgrService_GetDataTypeConfigs_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.GetDataTypeConfigsRsp{}, rsp)
	rsp, err = pb.CollectMgrService_GetDataTypeConfigWithFields_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.GetDataTypeConfigWithFieldsRsp{}, rsp)
	rsp, err = pb.CollectMgrService_RecalculateAllTaskInstances_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.RecalculateAllTaskInstancesRsp{}, rsp)
}

func TestUnimplementedCollectMgr_ShouldReturnErrors(t *testing.T) {
	svc := &pb.UnimplementedCollectMgr{}
	ctx := context.Background()
	_, err := svc.GetTaskRuleList(ctx, &pb.GetTaskRuleListReq{})
	assert.Error(t, err)
	_, err = svc.GetTaskRuleDetail(ctx, &pb.GetTaskRuleDetailReq{})
	assert.Error(t, err)
	_, err = svc.CreateTaskRule(ctx, &pb.CreateTaskRuleReq{})
	assert.Error(t, err)
	_, err = svc.UpdateTaskRule(ctx, &pb.UpdateTaskRuleReq{})
	assert.Error(t, err)
	_, err = svc.DisableTaskRule(ctx, &pb.DisableTaskRuleReq{})
	assert.Error(t, err)
}

func TestRegisterCollectMgrService_ShouldRegisterWithoutPanic(t *testing.T) {
	stub := &CollectMgrServiceStub{}
	s := &fakeTRPCService{}
	require.NotPanics(t, func() {
		pb.RegisterCollectMgrService(s, stub)
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
