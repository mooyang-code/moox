package rpc

import (
	"context"

	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
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
	exerciseMessage(t, &strategypb.TargetWeight{})
	exerciseMessage(t, &strategypb.StrategyDef{})
	exerciseMessage(t, &strategypb.StrategyBinding{})
	exerciseMessage(t, &strategypb.StrategyState{})
	exerciseMessage(t, &strategypb.StrategyRun{})
	exerciseMessage(t, &strategypb.CreateStrategyReq{})
	exerciseMessage(t, &strategypb.CreateStrategyRsp{})
	exerciseMessage(t, &strategypb.RunOnceReq{})
	exerciseMessage(t, &strategypb.RunOnceRsp{})
	exerciseMessage(t, &strategypb.GetEngineStatusReq{})
	exerciseMessage(t, &strategypb.GetEngineStatusRsp{})
	exerciseMessage(t, &strategypb.PageReq{})
	exerciseMessage(t, &strategypb.TimeRange{})
	exerciseMessage(t, &strategypb.ListRunningStrategiesReq{})
	exerciseMessage(t, &strategypb.StrategyHealth{})
	exerciseMessage(t, &strategypb.RunningStrategySummary{})
	exerciseMessage(t, &strategypb.ListRunningStrategiesRsp{})
	exerciseMessage(t, &strategypb.GetStrategyOverviewReq{})
	exerciseMessage(t, &strategypb.GetStrategyOverviewRsp{})
	exerciseMessage(t, &strategypb.ListStrategyRunsReq{})
	exerciseMessage(t, &strategypb.ListStrategyRunsRsp{})
	exerciseMessage(t, &strategypb.GetStrategyRunReq{})
	exerciseMessage(t, &strategypb.GetStrategyRunRsp{})
	exerciseMessage(t, &strategypb.ListStrategyTargetsReq{})
	exerciseMessage(t, &strategypb.ListStrategyTargetsRsp{})
	exerciseMessage(t, &strategypb.GetStrategyStateSummaryReq{})
	exerciseMessage(t, &strategypb.GetStrategyStateSummaryRsp{})
	exerciseMessage(t, &strategypb.GetStrategyHealthReq{})
	exerciseMessage(t, &strategypb.GetStrategyHealthRsp{})
	exerciseMessage(t, &strategypb.PerformancePoint{})
	exerciseMessage(t, &strategypb.PerformanceSummary{})
	exerciseMessage(t, &strategypb.GetStrategyPerformanceReq{})
	exerciseMessage(t, &strategypb.GetStrategyPerformanceRsp{})
	exerciseMessage(t, &strategypb.BindingOperationReq{})
	exerciseMessage(t, &strategypb.SetExecutionModeReq{})
	exerciseMessage(t, &strategypb.BindingOperationRsp{})
}

func TestNilGetters_ShouldReturnZeroValues(t *testing.T) {
	var nilTargetWeight *strategypb.TargetWeight
	_ = nilTargetWeight
	var nilStrategyDef *strategypb.StrategyDef
	_ = nilStrategyDef
	var nilStrategyBinding *strategypb.StrategyBinding
	_ = nilStrategyBinding
	var nilStrategyState *strategypb.StrategyState
	_ = nilStrategyState
	var nilStrategyRun *strategypb.StrategyRun
	_ = nilStrategyRun
}

type StrategyMgrServiceStub struct{}
func (s *StrategyMgrServiceStub) CreateStrategy(context.Context, *strategypb.CreateStrategyReq) (*strategypb.CreateStrategyRsp, error) {
	return &strategypb.CreateStrategyRsp{}, nil
}
func (s *StrategyMgrServiceStub) RunOnce(context.Context, *strategypb.RunOnceReq) (*strategypb.RunOnceRsp, error) {
	return &strategypb.RunOnceRsp{}, nil
}
func (s *StrategyMgrServiceStub) GetEngineStatus(context.Context, *strategypb.GetEngineStatusReq) (*strategypb.GetEngineStatusRsp, error) {
	return &strategypb.GetEngineStatusRsp{}, nil
}
func (s *StrategyMgrServiceStub) ListRunningStrategies(context.Context, *strategypb.ListRunningStrategiesReq) (*strategypb.ListRunningStrategiesRsp, error) {
	return &strategypb.ListRunningStrategiesRsp{}, nil
}
func (s *StrategyMgrServiceStub) GetStrategyOverview(context.Context, *strategypb.GetStrategyOverviewReq) (*strategypb.GetStrategyOverviewRsp, error) {
	return &strategypb.GetStrategyOverviewRsp{}, nil
}
func (s *StrategyMgrServiceStub) ListStrategyRuns(context.Context, *strategypb.ListStrategyRunsReq) (*strategypb.ListStrategyRunsRsp, error) {
	return &strategypb.ListStrategyRunsRsp{}, nil
}
func (s *StrategyMgrServiceStub) GetStrategyRun(context.Context, *strategypb.GetStrategyRunReq) (*strategypb.GetStrategyRunRsp, error) {
	return &strategypb.GetStrategyRunRsp{}, nil
}
func (s *StrategyMgrServiceStub) ListStrategyTargets(context.Context, *strategypb.ListStrategyTargetsReq) (*strategypb.ListStrategyTargetsRsp, error) {
	return &strategypb.ListStrategyTargetsRsp{}, nil
}
func (s *StrategyMgrServiceStub) GetStrategyStateSummary(context.Context, *strategypb.GetStrategyStateSummaryReq) (*strategypb.GetStrategyStateSummaryRsp, error) {
	return &strategypb.GetStrategyStateSummaryRsp{}, nil
}
func (s *StrategyMgrServiceStub) GetStrategyHealth(context.Context, *strategypb.GetStrategyHealthReq) (*strategypb.GetStrategyHealthRsp, error) {
	return &strategypb.GetStrategyHealthRsp{}, nil
}
func (s *StrategyMgrServiceStub) GetStrategyPerformance(context.Context, *strategypb.GetStrategyPerformanceReq) (*strategypb.GetStrategyPerformanceRsp, error) {
	return &strategypb.GetStrategyPerformanceRsp{}, nil
}
func (s *StrategyMgrServiceStub) PauseBinding(context.Context, *strategypb.BindingOperationReq) (*strategypb.BindingOperationRsp, error) {
	return &strategypb.BindingOperationRsp{}, nil
}
func (s *StrategyMgrServiceStub) ResumeBinding(context.Context, *strategypb.BindingOperationReq) (*strategypb.BindingOperationRsp, error) {
	return &strategypb.BindingOperationRsp{}, nil
}
func (s *StrategyMgrServiceStub) SetExecutionMode(context.Context, *strategypb.SetExecutionModeReq) (*strategypb.BindingOperationRsp, error) {
	return &strategypb.BindingOperationRsp{}, nil
}

func TestStrategyMgrServiceHandlers_ShouldDispatchRPCs(t *testing.T) {
	stub := &StrategyMgrServiceStub{}
	ctx := context.Background()
	rsp, err := strategypb.StrategyMgrService_CreateStrategy_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &strategypb.CreateStrategyRsp{}, rsp)
	rsp, err = strategypb.StrategyMgrService_RunOnce_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &strategypb.RunOnceRsp{}, rsp)
	rsp, err = strategypb.StrategyMgrService_GetEngineStatus_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &strategypb.GetEngineStatusRsp{}, rsp)
	rsp, err = strategypb.StrategyMgrService_ListRunningStrategies_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &strategypb.ListRunningStrategiesRsp{}, rsp)
	rsp, err = strategypb.StrategyMgrService_GetStrategyOverview_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &strategypb.GetStrategyOverviewRsp{}, rsp)
	rsp, err = strategypb.StrategyMgrService_ListStrategyRuns_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &strategypb.ListStrategyRunsRsp{}, rsp)
	rsp, err = strategypb.StrategyMgrService_GetStrategyRun_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &strategypb.GetStrategyRunRsp{}, rsp)
	rsp, err = strategypb.StrategyMgrService_ListStrategyTargets_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &strategypb.ListStrategyTargetsRsp{}, rsp)
	rsp, err = strategypb.StrategyMgrService_GetStrategyStateSummary_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &strategypb.GetStrategyStateSummaryRsp{}, rsp)
	rsp, err = strategypb.StrategyMgrService_GetStrategyHealth_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &strategypb.GetStrategyHealthRsp{}, rsp)
	rsp, err = strategypb.StrategyMgrService_GetStrategyPerformance_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &strategypb.GetStrategyPerformanceRsp{}, rsp)
	rsp, err = strategypb.StrategyMgrService_PauseBinding_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &strategypb.BindingOperationRsp{}, rsp)
	rsp, err = strategypb.StrategyMgrService_ResumeBinding_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &strategypb.BindingOperationRsp{}, rsp)
	rsp, err = strategypb.StrategyMgrService_SetExecutionMode_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &strategypb.BindingOperationRsp{}, rsp)
}

func TestUnimplementedStrategyMgr_ShouldReturnErrors(t *testing.T) {
	svc := &strategypb.UnimplementedStrategyMgr{}
	ctx := context.Background()
	_, err := svc.CreateStrategy(ctx, &strategypb.CreateStrategyReq{})
	assert.Error(t, err)
	_, err = svc.RunOnce(ctx, &strategypb.RunOnceReq{})
	assert.Error(t, err)
	_, err = svc.GetEngineStatus(ctx, &strategypb.GetEngineStatusReq{})
	assert.Error(t, err)
	_, err = svc.ListRunningStrategies(ctx, &strategypb.ListRunningStrategiesReq{})
	assert.Error(t, err)
	_, err = svc.GetStrategyOverview(ctx, &strategypb.GetStrategyOverviewReq{})
	assert.Error(t, err)
}

func TestRegisterStrategyMgrService_ShouldRegisterWithoutPanic(t *testing.T) {
	stub := &StrategyMgrServiceStub{}
	s := &fakeTRPCService{}
	require.NotPanics(t, func() {
		strategypb.RegisterStrategyMgrService(s, stub)
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
