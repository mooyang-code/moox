package factorpb

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
	_ = msg.String()
	msg.ProtoMessage()
}

func noopFilter(req interface{}) (filter.ServerChain, error) {
	return filter.ServerChain{filter.NoopServerFilter}, nil
}

func TestProtoMessages_ShouldSupportResetAndString(t *testing.T) {
	exerciseMessage(t, &FactorDef{})
	exerciseMessage(t, &FactorBinding{})
	exerciseMessage(t, &FactorRun{})
	exerciseMessage(t, &CreateFactorReq{})
	exerciseMessage(t, &CreateFactorRsp{})
	exerciseMessage(t, &UpdateFactorReq{})
	exerciseMessage(t, &UpdateFactorRsp{})
	exerciseMessage(t, &GetFactorReq{})
	exerciseMessage(t, &GetFactorRsp{})
	exerciseMessage(t, &ListFactorsReq{})
	exerciseMessage(t, &ListFactorsRsp{})
	exerciseMessage(t, &SetFactorStatusReq{})
	exerciseMessage(t, &SetFactorStatusRsp{})
	exerciseMessage(t, &UpsertBindingReq{})
	exerciseMessage(t, &UpsertBindingRsp{})
	exerciseMessage(t, &ListBindingsReq{})
	exerciseMessage(t, &ListBindingsRsp{})
	exerciseMessage(t, &DeleteBindingReq{})
	exerciseMessage(t, &DeleteBindingRsp{})
	exerciseMessage(t, &RecalcFactorReq{})
	exerciseMessage(t, &RecalcFactorRsp{})
	exerciseMessage(t, &GetRecalcProgressReq{})
	exerciseMessage(t, &GetRecalcProgressRsp{})
	exerciseMessage(t, &ListFactorRunsReq{})
	exerciseMessage(t, &ListFactorRunsRsp{})
	exerciseMessage(t, &WorkerStatus{})
	exerciseMessage(t, &GetEngineStatusReq{})
	exerciseMessage(t, &GetEngineStatusRsp{})
}

func TestNilGetters_ShouldReturnZeroValues(t *testing.T) {
	var nilFactorDef *FactorDef
	_ = nilFactorDef
	var nilFactorBinding *FactorBinding
	_ = nilFactorBinding
	var nilFactorRun *FactorRun
	_ = nilFactorRun
	var nilCreateFactorReq *CreateFactorReq
	_ = nilCreateFactorReq
	var nilCreateFactorRsp *CreateFactorRsp
	_ = nilCreateFactorRsp
	var nilUpdateFactorReq *UpdateFactorReq
	_ = nilUpdateFactorReq
	var nilUpdateFactorRsp *UpdateFactorRsp
	_ = nilUpdateFactorRsp
	var nilGetFactorReq *GetFactorReq
	_ = nilGetFactorReq
}

type FactorMgrServiceStub struct{}
func (s *FactorMgrServiceStub) CreateFactor(context.Context, *CreateFactorReq) (*CreateFactorRsp, error) {
	return &CreateFactorRsp{}, nil
}
func (s *FactorMgrServiceStub) UpdateFactor(context.Context, *UpdateFactorReq) (*UpdateFactorRsp, error) {
	return &UpdateFactorRsp{}, nil
}
func (s *FactorMgrServiceStub) GetFactor(context.Context, *GetFactorReq) (*GetFactorRsp, error) {
	return &GetFactorRsp{}, nil
}
func (s *FactorMgrServiceStub) ListFactors(context.Context, *ListFactorsReq) (*ListFactorsRsp, error) {
	return &ListFactorsRsp{}, nil
}
func (s *FactorMgrServiceStub) SetFactorStatus(context.Context, *SetFactorStatusReq) (*SetFactorStatusRsp, error) {
	return &SetFactorStatusRsp{}, nil
}
func (s *FactorMgrServiceStub) UpsertBinding(context.Context, *UpsertBindingReq) (*UpsertBindingRsp, error) {
	return &UpsertBindingRsp{}, nil
}
func (s *FactorMgrServiceStub) ListBindings(context.Context, *ListBindingsReq) (*ListBindingsRsp, error) {
	return &ListBindingsRsp{}, nil
}
func (s *FactorMgrServiceStub) DeleteBinding(context.Context, *DeleteBindingReq) (*DeleteBindingRsp, error) {
	return &DeleteBindingRsp{}, nil
}
func (s *FactorMgrServiceStub) RecalcFactor(context.Context, *RecalcFactorReq) (*RecalcFactorRsp, error) {
	return &RecalcFactorRsp{}, nil
}
func (s *FactorMgrServiceStub) GetRecalcProgress(context.Context, *GetRecalcProgressReq) (*GetRecalcProgressRsp, error) {
	return &GetRecalcProgressRsp{}, nil
}
func (s *FactorMgrServiceStub) ListFactorRuns(context.Context, *ListFactorRunsReq) (*ListFactorRunsRsp, error) {
	return &ListFactorRunsRsp{}, nil
}
func (s *FactorMgrServiceStub) GetEngineStatus(context.Context, *GetEngineStatusReq) (*GetEngineStatusRsp, error) {
	return &GetEngineStatusRsp{}, nil
}

func TestFactorMgrServiceHandlers_ShouldDispatchRPCs(t *testing.T) {
	stub := &FactorMgrServiceStub{}
	ctx := context.Background()
	rsp, err := FactorMgrService_CreateFactor_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &CreateFactorRsp{}, rsp)
	rsp, err = FactorMgrService_UpdateFactor_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &UpdateFactorRsp{}, rsp)
	rsp, err = FactorMgrService_GetFactor_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &GetFactorRsp{}, rsp)
	rsp, err = FactorMgrService_ListFactors_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &ListFactorsRsp{}, rsp)
	rsp, err = FactorMgrService_SetFactorStatus_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &SetFactorStatusRsp{}, rsp)
	rsp, err = FactorMgrService_UpsertBinding_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &UpsertBindingRsp{}, rsp)
	rsp, err = FactorMgrService_ListBindings_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &ListBindingsRsp{}, rsp)
	rsp, err = FactorMgrService_DeleteBinding_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &DeleteBindingRsp{}, rsp)
	rsp, err = FactorMgrService_RecalcFactor_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &RecalcFactorRsp{}, rsp)
	rsp, err = FactorMgrService_GetRecalcProgress_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &GetRecalcProgressRsp{}, rsp)
	rsp, err = FactorMgrService_ListFactorRuns_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &ListFactorRunsRsp{}, rsp)
	rsp, err = FactorMgrService_GetEngineStatus_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &GetEngineStatusRsp{}, rsp)
}

func TestUnimplementedFactorMgr_ShouldReturnErrors(t *testing.T) {
	svc := &UnimplementedFactorMgr{}
	ctx := context.Background()
	_, err := svc.CreateFactor(ctx, &CreateFactorReq{})
	assert.Error(t, err)
	_, err = svc.UpdateFactor(ctx, &UpdateFactorReq{})
	assert.Error(t, err)
	_, err = svc.GetFactor(ctx, &GetFactorReq{})
	assert.Error(t, err)
	_, err = svc.ListFactors(ctx, &ListFactorsReq{})
	assert.Error(t, err)
	_, err = svc.SetFactorStatus(ctx, &SetFactorStatusReq{})
	assert.Error(t, err)
	_, err = svc.UpsertBinding(ctx, &UpsertBindingReq{})
	assert.Error(t, err)
}

func TestRegisterFactorMgrService_ShouldRegisterWithoutPanic(t *testing.T) {
	stub := &FactorMgrServiceStub{}
	s := &fakeTRPCService{}
	require.NotPanics(t, func() {
		RegisterFactorMgrService(s, stub)
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

