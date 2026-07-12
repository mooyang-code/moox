package rpc

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFactorCRUD_AndBindingsLifecycle(t *testing.T) {
	svc := newRPCTestService(t)
	ctx := context.Background()

	create, err := svc.CreateFactor(ctx, &factorpb.CreateFactorReq{Factor: &factorpb.FactorDef{
		FactorId: "bias", Name: "Bias", SourceCode: "def signal(df): return df", ParamsJson: "[20]", Status: "enabled",
	}})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, create.GetRetInfo().GetCode())

	got, err := svc.GetFactor(ctx, &factorpb.GetFactorReq{FactorId: "bias"})
	require.NoError(t, err)
	assert.Equal(t, "bias", got.GetFactor().GetFactorId())

	_, err = svc.GetFactor(ctx, &factorpb.GetFactorReq{})
	require.NoError(t, err)

	update, err := svc.UpdateFactor(ctx, &factorpb.UpdateFactorReq{FactorId: "bias", Factor: &factorpb.FactorDef{
		Name: "Bias", SourceCode: "def signal(df, n): return df", ParamsJson: "[10]", Status: "enabled",
	}})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_SUCCESS, update.GetRetInfo().GetCode())

	listed, err := svc.ListFactors(ctx, &factorpb.ListFactorsReq{Page: &commonpb.Page{Page: 1, Size: 10}})
	require.NoError(t, err)
	assert.NotEmpty(t, listed.GetFactors())

	status, err := svc.SetFactorStatus(ctx, &factorpb.SetFactorStatusReq{FactorId: "bias", Status: "disabled"})
	require.NoError(t, err)
	assert.Equal(t, "disabled", status.GetFactor().GetStatus())

	_, err = svc.SetFactorStatus(ctx, &factorpb.SetFactorStatusReq{})
	require.NoError(t, err)

	_, err = svc.CreateFactor(ctx, &factorpb.CreateFactorReq{Factor: &factorpb.FactorDef{
		FactorId: "bias2", Name: "Bias", SourceCode: "x", Status: "enabled",
	}})
	require.NoError(t, err)

	bind, err := svc.UpsertBinding(ctx, &factorpb.UpsertBindingReq{Binding: &factorpb.FactorBinding{
		FactorId: "bias", SpaceId: "crypto", SourceDataset: "kline", Freq: "1m", Status: "enabled",
	}})
	require.NoError(t, err)
	bindingID := bind.GetBinding().GetBindingId()
	require.NotEmpty(t, bindingID)

	bindings, err := svc.ListBindings(ctx, &factorpb.ListBindingsReq{SpaceId: "crypto", Page: &commonpb.Page{Page: 1, Size: 10}})
	require.NoError(t, err)
	assert.NotEmpty(t, bindings.GetBindings())

	del, err := svc.DeleteBinding(ctx, &factorpb.DeleteBindingReq{BindingId: bindingID})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_SUCCESS, del.GetRetInfo().GetCode())

	_, err = svc.DeleteBinding(ctx, &factorpb.DeleteBindingReq{})
	require.NoError(t, err)
}

func TestGetEngineStatus_WithRuntimeProviders(t *testing.T) {
	db := openRPCTestDB(t)
	sched := newFakeRPCScheduler()
	eng := &fakeEngineStatus{workers: 2}
	svc := NewWithRuntime(db, sched, eng, WithFactorsDir(t.TempDir()))

	rsp, err := svc.GetEngineStatus(context.Background(), &factorpb.GetEngineStatusReq{})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Len(t, rsp.GetWorkers(), 2)
	assert.Equal(t, int32(sched.Status().QueueDepth), rsp.GetQueueDepth())
}

func TestGetRecalcProgress_Validation(t *testing.T) {
	svc := newRPCTestService(t)
	rsp, err := svc.GetRecalcProgress(context.Background(), &factorpb.GetRecalcProgressReq{})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())

	rsp, err = svc.GetRecalcProgress(context.Background(), &factorpb.GetRecalcProgressReq{RecalcId: "missing"})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestRecalcFactor_ValidationBranches(t *testing.T) {
	svc := New(openRPCTestDB(t))
	rsp, err := svc.RecalcFactor(context.Background(), &factorpb.RecalcFactorReq{
		SpaceId: "s", SourceDataset: "d", Freq: "1m", SubjectId: "BTC",
	})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_INNER_ERR, rsp.GetRetInfo().GetCode())

	svc = newRPCTestService(t)
	rsp, err = svc.RecalcFactor(context.Background(), &factorpb.RecalcFactorReq{})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())

	rsp, err = svc.RecalcFactor(context.Background(), &factorpb.RecalcFactorReq{
		SpaceId: "s", SourceDataset: "d", Freq: "1m",
	})
	require.NoError(t, err)
	assert.Contains(t, rsp.GetRetInfo().GetMsg(), "subject_id")
}

type fakeEngineStatus struct {
	workers int
}

func (f *fakeEngineStatus) Status() engine.WorkerPoolStatus {
	return engine.WorkerPoolStatus{Workers: f.workers}
}
