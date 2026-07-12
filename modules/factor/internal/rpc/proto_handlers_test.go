package rpc

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/filter"
)

func noopTRPCFilter(req interface{}) (filter.ServerChain, error) {
	return filter.ServerChain{filter.NoopServerFilter}, nil
}

type FactorMgrServiceStub struct{}
func (s *FactorMgrServiceStub) CreateFactor(context.Context, *pb.CreateFactorReq) (*pb.CreateFactorRsp, error) { return &pb.CreateFactorRsp{}, nil }
func (s *FactorMgrServiceStub) UpdateFactor(context.Context, *pb.UpdateFactorReq) (*pb.UpdateFactorRsp, error) { return &pb.UpdateFactorRsp{}, nil }
func (s *FactorMgrServiceStub) GetFactor(context.Context, *pb.GetFactorReq) (*pb.GetFactorRsp, error) { return &pb.GetFactorRsp{}, nil }
func (s *FactorMgrServiceStub) ListFactors(context.Context, *pb.ListFactorsReq) (*pb.ListFactorsRsp, error) { return &pb.ListFactorsRsp{}, nil }
func (s *FactorMgrServiceStub) SetFactorStatus(context.Context, *pb.SetFactorStatusReq) (*pb.SetFactorStatusRsp, error) { return &pb.SetFactorStatusRsp{}, nil }
func (s *FactorMgrServiceStub) UpsertBinding(context.Context, *pb.UpsertBindingReq) (*pb.UpsertBindingRsp, error) { return &pb.UpsertBindingRsp{}, nil }
func (s *FactorMgrServiceStub) ListBindings(context.Context, *pb.ListBindingsReq) (*pb.ListBindingsRsp, error) { return &pb.ListBindingsRsp{}, nil }
func (s *FactorMgrServiceStub) DeleteBinding(context.Context, *pb.DeleteBindingReq) (*pb.DeleteBindingRsp, error) { return &pb.DeleteBindingRsp{}, nil }
func (s *FactorMgrServiceStub) RecalcFactor(context.Context, *pb.RecalcFactorReq) (*pb.RecalcFactorRsp, error) { return &pb.RecalcFactorRsp{}, nil }
func (s *FactorMgrServiceStub) GetRecalcProgress(context.Context, *pb.GetRecalcProgressReq) (*pb.GetRecalcProgressRsp, error) { return &pb.GetRecalcProgressRsp{}, nil }
func (s *FactorMgrServiceStub) ListFactorRuns(context.Context, *pb.ListFactorRunsReq) (*pb.ListFactorRunsRsp, error) { return &pb.ListFactorRunsRsp{}, nil }
func (s *FactorMgrServiceStub) GetEngineStatus(context.Context, *pb.GetEngineStatusReq) (*pb.GetEngineStatusRsp, error) { return &pb.GetEngineStatusRsp{}, nil }

func TestFactorMgrServiceHandlers_ShouldDispatch(t *testing.T) {
	stub := &FactorMgrServiceStub{}
	ctx := context.Background()
	rsp, err := pb.FactorMgrService_CreateFactor_Handler(stub, ctx, noopTRPCFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.CreateFactorRsp{}, rsp)
	rsp, err = pb.FactorMgrService_UpdateFactor_Handler(stub, ctx, noopTRPCFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.UpdateFactorRsp{}, rsp)
	rsp, err = pb.FactorMgrService_GetFactor_Handler(stub, ctx, noopTRPCFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.GetFactorRsp{}, rsp)
	rsp, err = pb.FactorMgrService_ListFactors_Handler(stub, ctx, noopTRPCFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.ListFactorsRsp{}, rsp)
	rsp, err = pb.FactorMgrService_SetFactorStatus_Handler(stub, ctx, noopTRPCFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.SetFactorStatusRsp{}, rsp)
	rsp, err = pb.FactorMgrService_UpsertBinding_Handler(stub, ctx, noopTRPCFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.UpsertBindingRsp{}, rsp)
	rsp, err = pb.FactorMgrService_ListBindings_Handler(stub, ctx, noopTRPCFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.ListBindingsRsp{}, rsp)
	rsp, err = pb.FactorMgrService_DeleteBinding_Handler(stub, ctx, noopTRPCFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.DeleteBindingRsp{}, rsp)
	rsp, err = pb.FactorMgrService_RecalcFactor_Handler(stub, ctx, noopTRPCFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.RecalcFactorRsp{}, rsp)
	rsp, err = pb.FactorMgrService_GetRecalcProgress_Handler(stub, ctx, noopTRPCFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.GetRecalcProgressRsp{}, rsp)
	rsp, err = pb.FactorMgrService_ListFactorRuns_Handler(stub, ctx, noopTRPCFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.ListFactorRunsRsp{}, rsp)
	rsp, err = pb.FactorMgrService_GetEngineStatus_Handler(stub, ctx, noopTRPCFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.GetEngineStatusRsp{}, rsp)
}

func TestUnimplementedFactorMgr_AllMethods(t *testing.T) {
	svc := &pb.UnimplementedFactorMgr{}
	ctx := context.Background()
	_, err := svc.CreateFactor(ctx, &pb.CreateFactorReq{})
	assert.Error(t, err)
	_, err = svc.UpdateFactor(ctx, &pb.UpdateFactorReq{})
	assert.Error(t, err)
	_, err = svc.GetFactor(ctx, &pb.GetFactorReq{})
	assert.Error(t, err)
	_, err = svc.ListFactors(ctx, &pb.ListFactorsReq{})
	assert.Error(t, err)
	_, err = svc.SetFactorStatus(ctx, &pb.SetFactorStatusReq{})
	assert.Error(t, err)
	_, err = svc.UpsertBinding(ctx, &pb.UpsertBindingReq{})
	assert.Error(t, err)
	_, err = svc.ListBindings(ctx, &pb.ListBindingsReq{})
	assert.Error(t, err)
	_, err = svc.DeleteBinding(ctx, &pb.DeleteBindingReq{})
	assert.Error(t, err)
	_, err = svc.RecalcFactor(ctx, &pb.RecalcFactorReq{})
	assert.Error(t, err)
	_, err = svc.GetRecalcProgress(ctx, &pb.GetRecalcProgressReq{})
	assert.Error(t, err)
	_, err = svc.ListFactorRuns(ctx, &pb.ListFactorRunsReq{})
	assert.Error(t, err)
	_, err = svc.GetEngineStatus(ctx, &pb.GetEngineStatusReq{})
	assert.Error(t, err)
}
