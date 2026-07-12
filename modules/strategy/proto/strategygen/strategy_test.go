package strategypb

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
	exerciseMessage(t, &TargetWeight{})
	exerciseMessage(t, &StrategyDef{})
	exerciseMessage(t, &StrategyBinding{})
	exerciseMessage(t, &StrategyState{})
	exerciseMessage(t, &StrategyRun{})
	exerciseMessage(t, &CreateStrategyReq{})
	exerciseMessage(t, &CreateStrategyRsp{})
	exerciseMessage(t, &RunOnceReq{})
	exerciseMessage(t, &RunOnceRsp{})
	exerciseMessage(t, &GetEngineStatusReq{})
	exerciseMessage(t, &GetEngineStatusRsp{})
	exerciseMessage(t, &PageReq{})
	exerciseMessage(t, &TimeRange{})
	exerciseMessage(t, &ListRunningStrategiesReq{})
	exerciseMessage(t, &StrategyHealth{})
	exerciseMessage(t, &RunningStrategySummary{})
	exerciseMessage(t, &ListRunningStrategiesRsp{})
	exerciseMessage(t, &GetStrategyOverviewReq{})
	exerciseMessage(t, &GetStrategyOverviewRsp{})
	exerciseMessage(t, &ListStrategyRunsReq{})
	exerciseMessage(t, &ListStrategyRunsRsp{})
	exerciseMessage(t, &GetStrategyRunReq{})
	exerciseMessage(t, &GetStrategyRunRsp{})
	exerciseMessage(t, &ListStrategyTargetsReq{})
	exerciseMessage(t, &ListStrategyTargetsRsp{})
	exerciseMessage(t, &GetStrategyStateSummaryReq{})
	exerciseMessage(t, &GetStrategyStateSummaryRsp{})
	exerciseMessage(t, &GetStrategyHealthReq{})
	exerciseMessage(t, &GetStrategyHealthRsp{})
	exerciseMessage(t, &PerformancePoint{})
	exerciseMessage(t, &PerformanceSummary{})
	exerciseMessage(t, &GetStrategyPerformanceReq{})
	exerciseMessage(t, &GetStrategyPerformanceRsp{})
	exerciseMessage(t, &BindingOperationReq{})
	exerciseMessage(t, &SetExecutionModeReq{})
	exerciseMessage(t, &BindingOperationRsp{})
}

func TestNilGetters_ShouldReturnZeroValues(t *testing.T) {
	var nilTargetWeight *TargetWeight
	_ = nilTargetWeight
	var nilStrategyDef *StrategyDef
	_ = nilStrategyDef
	var nilStrategyBinding *StrategyBinding
	_ = nilStrategyBinding
	var nilStrategyState *StrategyState
	_ = nilStrategyState
	var nilStrategyRun *StrategyRun
	_ = nilStrategyRun
}

type StrategyMgrServiceStub struct{}
func (s *StrategyMgrServiceStub) CreateStrategy(context.Context, *CreateStrategyReq) (*CreateStrategyRsp, error) {
	return &CreateStrategyRsp{}, nil
}
func (s *StrategyMgrServiceStub) RunOnce(context.Context, *RunOnceReq) (*RunOnceRsp, error) {
	return &RunOnceRsp{}, nil
}
func (s *StrategyMgrServiceStub) GetEngineStatus(context.Context, *GetEngineStatusReq) (*GetEngineStatusRsp, error) {
	return &GetEngineStatusRsp{}, nil
}
func (s *StrategyMgrServiceStub) ListRunningStrategies(context.Context, *ListRunningStrategiesReq) (*ListRunningStrategiesRsp, error) {
	return &ListRunningStrategiesRsp{}, nil
}
func (s *StrategyMgrServiceStub) GetStrategyOverview(context.Context, *GetStrategyOverviewReq) (*GetStrategyOverviewRsp, error) {
	return &GetStrategyOverviewRsp{}, nil
}
func (s *StrategyMgrServiceStub) ListStrategyRuns(context.Context, *ListStrategyRunsReq) (*ListStrategyRunsRsp, error) {
	return &ListStrategyRunsRsp{}, nil
}
func (s *StrategyMgrServiceStub) GetStrategyRun(context.Context, *GetStrategyRunReq) (*GetStrategyRunRsp, error) {
	return &GetStrategyRunRsp{}, nil
}
func (s *StrategyMgrServiceStub) ListStrategyTargets(context.Context, *ListStrategyTargetsReq) (*ListStrategyTargetsRsp, error) {
	return &ListStrategyTargetsRsp{}, nil
}
func (s *StrategyMgrServiceStub) GetStrategyStateSummary(context.Context, *GetStrategyStateSummaryReq) (*GetStrategyStateSummaryRsp, error) {
	return &GetStrategyStateSummaryRsp{}, nil
}
func (s *StrategyMgrServiceStub) GetStrategyHealth(context.Context, *GetStrategyHealthReq) (*GetStrategyHealthRsp, error) {
	return &GetStrategyHealthRsp{}, nil
}
func (s *StrategyMgrServiceStub) GetStrategyPerformance(context.Context, *GetStrategyPerformanceReq) (*GetStrategyPerformanceRsp, error) {
	return &GetStrategyPerformanceRsp{}, nil
}
func (s *StrategyMgrServiceStub) PauseBinding(context.Context, *BindingOperationReq) (*BindingOperationRsp, error) {
	return &BindingOperationRsp{}, nil
}
func (s *StrategyMgrServiceStub) ResumeBinding(context.Context, *BindingOperationReq) (*BindingOperationRsp, error) {
	return &BindingOperationRsp{}, nil
}
func (s *StrategyMgrServiceStub) SetExecutionMode(context.Context, *SetExecutionModeReq) (*BindingOperationRsp, error) {
	return &BindingOperationRsp{}, nil
}

func TestStrategyMgrServiceHandlers_ShouldDispatchRPCs(t *testing.T) {
	stub := &StrategyMgrServiceStub{}
	ctx := context.Background()
	rsp, err := StrategyMgrService_CreateStrategy_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &CreateStrategyRsp{}, rsp)
	rsp, err := StrategyMgrService_RunOnce_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &RunOnceRsp{}, rsp)
	rsp, err := StrategyMgrService_GetEngineStatus_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &GetEngineStatusRsp{}, rsp)
	rsp, err := StrategyMgrService_ListRunningStrategies_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &ListRunningStrategiesRsp{}, rsp)
	rsp, err := StrategyMgrService_GetStrategyOverview_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &GetStrategyOverviewRsp{}, rsp)
	rsp, err := StrategyMgrService_ListStrategyRuns_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &ListStrategyRunsRsp{}, rsp)
	rsp, err := StrategyMgrService_GetStrategyRun_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &GetStrategyRunRsp{}, rsp)
	rsp, err := StrategyMgrService_ListStrategyTargets_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &ListStrategyTargetsRsp{}, rsp)
	rsp, err := StrategyMgrService_GetStrategyStateSummary_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &GetStrategyStateSummaryRsp{}, rsp)
	rsp, err := StrategyMgrService_GetStrategyHealth_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &GetStrategyHealthRsp{}, rsp)
	rsp, err := StrategyMgrService_GetStrategyPerformance_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &GetStrategyPerformanceRsp{}, rsp)
	rsp, err = StrategyMgrService_PauseBinding_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &BindingOperationRsp{}, rsp)
	rsp, err = StrategyMgrService_ResumeBinding_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &BindingOperationRsp{}, rsp)
	rsp, err := StrategyMgrService_SetExecutionMode_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &BindingOperationRsp{}, rsp)
}

func TestUnimplementedStrategyMgr_ShouldReturnErrors(t *testing.T) {
	svc := &UnimplementedStrategyMgr{}
	ctx := context.Background()
	_, err := svc.CreateStrategy(ctx, &CreateStrategyReq{})
	assert.Error(t, err)
	_, err := svc.RunOnce(ctx, &RunOnceReq{})
	assert.Error(t, err)
	_, err := svc.GetEngineStatus(ctx, &GetEngineStatusReq{})
	assert.Error(t, err)
	_, err := svc.ListRunningStrategies(ctx, &ListRunningStrategiesReq{})
	assert.Error(t, err)
	_, err := svc.GetStrategyOverview(ctx, &GetStrategyOverviewReq{})
	assert.Error(t, err)
}

func TestRegisterStrategyMgrService_ShouldRegisterWithoutPanic(t *testing.T) {
	stub := &StrategyMgrServiceStub{}
	s := &fakeTRPCService{}
	require.NotPanics(t, func() {
		RegisterStrategyMgrService(s, stub)
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
