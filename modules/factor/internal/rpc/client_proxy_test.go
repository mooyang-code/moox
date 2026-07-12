package rpc

import (
	"context"
	"fmt"
	"testing"

	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/stretchr/testify/assert"
)

func TestFactorMgrClientProxy_AllMethods_ShouldInvoke(t *testing.T) {
	ctx := context.Background()
	proxy := factorpb.NewFactorMgrClientProxy()
	calls := []func() error{
		func() error { _, err := proxy.CreateFactor(ctx, &factorpb.CreateFactorReq{}); return err },
		func() error { _, err := proxy.UpdateFactor(ctx, &factorpb.UpdateFactorReq{}); return err },
		func() error { _, err := proxy.GetFactor(ctx, &factorpb.GetFactorReq{}); return err },
		func() error { _, err := proxy.ListFactors(ctx, &factorpb.ListFactorsReq{}); return err },
		func() error { _, err := proxy.SetFactorStatus(ctx, &factorpb.SetFactorStatusReq{}); return err },
		func() error { _, err := proxy.UpsertBinding(ctx, &factorpb.UpsertBindingReq{}); return err },
		func() error { _, err := proxy.ListBindings(ctx, &factorpb.ListBindingsReq{}); return err },
		func() error { _, err := proxy.DeleteBinding(ctx, &factorpb.DeleteBindingReq{}); return err },
		func() error { _, err := proxy.RecalcFactor(ctx, &factorpb.RecalcFactorReq{}); return err },
		func() error { _, err := proxy.GetRecalcProgress(ctx, &factorpb.GetRecalcProgressReq{}); return err },
		func() error { _, err := proxy.ListFactorRuns(ctx, &factorpb.ListFactorRunsReq{}); return err },
		func() error { _, err := proxy.GetEngineStatus(ctx, &factorpb.GetEngineStatusReq{}); return err },
	}
	for i, call := range calls {
		assert.Error(t, call(), fmt.Sprintf("call %d should fail without backend", i))
	}
}
