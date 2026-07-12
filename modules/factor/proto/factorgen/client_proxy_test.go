package factorpb

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClientProxy_AllMethods_ShouldInvoke(t *testing.T) {
	ctx := context.Background()
	proxy := NewFactorMgrClientProxy()
	calls := []func() error{
		func() error { _, err := proxy.CreateFactor(ctx, &CreateFactorReq{}); return err },
		func() error { _, err := proxy.UpdateFactor(ctx, &UpdateFactorReq{}); return err },
		func() error { _, err := proxy.GetFactor(ctx, &GetFactorReq{}); return err },
		func() error { _, err := proxy.ListFactors(ctx, &ListFactorsReq{}); return err },
		func() error { _, err := proxy.SetFactorStatus(ctx, &SetFactorStatusReq{}); return err },
		func() error { _, err := proxy.UpsertBinding(ctx, &UpsertBindingReq{}); return err },
		func() error { _, err := proxy.ListBindings(ctx, &ListBindingsReq{}); return err },
		func() error { _, err := proxy.DeleteBinding(ctx, &DeleteBindingReq{}); return err },
		func() error { _, err := proxy.RecalcFactor(ctx, &RecalcFactorReq{}); return err },
		func() error { _, err := proxy.GetRecalcProgress(ctx, &GetRecalcProgressReq{}); return err },
		func() error { _, err := proxy.ListFactorRuns(ctx, &ListFactorRunsReq{}); return err },
		func() error { _, err := proxy.GetEngineStatus(ctx, &GetEngineStatusReq{}); return err },
	}
	for i, call := range calls {
		assert.Error(t, call(), fmt.Sprintf("call %d should fail without backend", i))
	}
}

func TestUnimplementedFactorMgr_RemainingMethods_ShouldReturnErrors(t *testing.T) {
	svc := &UnimplementedFactorMgr{}
	ctx := context.Background()
	_, err := svc.ListBindings(ctx, &ListBindingsReq{})
	assert.Error(t, err)
	_, err = svc.DeleteBinding(ctx, &DeleteBindingReq{})
	assert.Error(t, err)
	_, err = svc.RecalcFactor(ctx, &RecalcFactorReq{})
	assert.Error(t, err)
	_, err = svc.GetRecalcProgress(ctx, &GetRecalcProgressReq{})
	assert.Error(t, err)
	_, err = svc.ListFactorRuns(ctx, &ListFactorRunsReq{})
	assert.Error(t, err)
	_, err = svc.GetEngineStatus(ctx, &GetEngineStatusReq{})
	assert.Error(t, err)
}
