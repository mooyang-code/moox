package rpc

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/scheduler"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
)

func TestCreateFactorUsesGenericContractAndComputedSourceHash(t *testing.T) {
	svc := NewWithRuntime(openRPCTestDB(t), nil, WithFactorsDir(t.TempDir()))
	factor := genericFactorPB("bias", "Bias", []string{"bias"})
	factor.SourceHash = "untrusted"
	rsp, err := svc.CreateFactor(context.Background(), &factorpb.CreateFactorReq{Factor: factor})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, []string{"close"}, rsp.GetFactor().GetInputColumns())
	require.Equal(t, []string{"bias"}, rsp.GetFactor().GetOutputs())
	require.NotEqual(t, "untrusted", rsp.GetFactor().GetSourceHash())
}

func TestCreateFactorRejectsDuplicateIDOrNameWithoutOverwritingOutputs(t *testing.T) {
	db := openRPCTestDB(t)
	svc := NewWithRuntime(db, nil, WithFactorsDir(t.TempDir()))
	first := genericFactorPB("factor-1", "First", []string{"original"})
	rsp, err := svc.CreateFactor(context.Background(), &factorpb.CreateFactorReq{Factor: first})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())

	duplicateID := genericFactorPB("factor-1", "First", []string{"changed"})
	rsp, err = svc.CreateFactor(context.Background(), &factorpb.CreateFactorReq{Factor: duplicateID})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	stored, err := db.Factors().Get(context.Background(), "factor-1")
	require.NoError(t, err)
	require.Equal(t, []string{"original"}, stored.Outputs)

	duplicateName := genericFactorPB("factor-2", "First", []string{"other"})
	rsp, err = svc.CreateFactor(context.Background(), &factorpb.CreateFactorReq{Factor: duplicateName})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestUpdateFactorRejectsOutputChangesButUpdatesMutableFields(t *testing.T) {
	db := openRPCTestDB(t)
	svc := NewWithRuntime(db, nil, WithFactorsDir(t.TempDir()))
	created := genericFactorPB("factor-1", "First", []string{"value"})
	rsp, err := svc.CreateFactor(context.Background(), &factorpb.CreateFactorReq{Factor: created})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())

	changed := genericFactorPB("factor-1", "First", []string{"changed"})
	updateRsp, err := svc.UpdateFactor(context.Background(), &factorpb.UpdateFactorReq{FactorId: "factor-1", Factor: changed})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, updateRsp.GetRetInfo().GetCode())

	mutable := genericFactorPB("factor-1", "Renamed", []string{"value"})
	mutable.ParamsJson = `{"window":10}`
	updateRsp, err = svc.UpdateFactor(context.Background(), &factorpb.UpdateFactorReq{FactorId: "factor-1", Factor: mutable})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, updateRsp.GetRetInfo().GetCode())
	require.Equal(t, "Renamed", updateRsp.GetFactor().GetName())
	require.Equal(t, `{"window":10}`, updateRsp.GetFactor().GetParamsJson())
}

func TestRecalcFactorRunsSynchronousRange(t *testing.T) {
	db := openRPCTestDB(t)
	seedRPCFactorAndBinding(t, db, domain.FactorStatusEnabled)
	runner := &fakeRPCScheduler{}
	svc := NewWithRuntime(db, runner, WithFactorsDir(t.TempDir()))
	rsp, err := svc.RecalcFactor(context.Background(), &factorpb.RecalcFactorReq{
		FactorId: "bias", SpaceId: "crypto", SourceDataset: "bars",
		SubjectId: "BTC", Freq: "1m",
		StartTime: "2026-07-26T00:00:00Z", EndTime: "2026-07-26T01:00:00Z",
	})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, runner.tasks, 1)
	require.Equal(t, time.Hour, runner.tasks[0].EndTime.Sub(runner.tasks[0].StartTime))
}

func TestRecalcRejectsMissingOrInvalidRange(t *testing.T) {
	svc := NewWithRuntime(openRPCTestDB(t), &fakeRPCScheduler{}, WithFactorsDir(t.TempDir()))
	for _, req := range []*factorpb.RecalcFactorReq{
		{SpaceId: "crypto", SourceDataset: "bars", SubjectId: "BTC", Freq: "1m"},
		{SpaceId: "crypto", SourceDataset: "bars", SubjectId: "BTC", Freq: "1m", StartTime: "bad", EndTime: "2026-07-26T01:00:00Z"},
		{SpaceId: "crypto", SourceDataset: "bars", SubjectId: "BTC", Freq: "1m", StartTime: "2026-07-26T01:00:00Z", EndTime: "2026-07-26T00:00:00Z"},
	} {
		rsp, err := svc.RecalcFactor(context.Background(), req)
		require.NoError(t, err)
		require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	}
}

func TestRecalcHonorsBindingSubjectScope(t *testing.T) {
	db := openRPCTestDB(t)
	seedRPCFactorAndBinding(t, db, domain.FactorStatusEnabled)
	require.NoError(t, db.Bindings().Upsert(context.Background(), domain.FactorBinding{
		BindingID: "bind", FactorID: "bias", SpaceID: "crypto", SourceDataset: "bars",
		Freq: "1m", SubjectMode: domain.SubjectModeInclude, SubjectsJSON: `["ETH"]`,
		TargetDataset: "bars_factor", Status: domain.BindingStatusEnabled,
	}))
	runner := &fakeRPCScheduler{}
	svc := NewWithRuntime(db, runner, WithFactorsDir(t.TempDir()))
	rsp, err := svc.RecalcFactor(context.Background(), &factorpb.RecalcFactorReq{
		FactorId: "bias", SpaceId: "crypto", SourceDataset: "bars",
		SubjectId: "BTC", Freq: "1m",
		StartTime: "2026-07-26T00:00:00Z", EndTime: "2026-07-26T01:00:00Z",
	})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	require.Empty(t, runner.tasks)
}

func TestGetEngineStatusIsMinimal(t *testing.T) {
	runner := &fakeRPCScheduler{status: scheduler.Status{QueueDepth: 3, QueueOverflowCount: 4}}
	svc := NewWithRuntime(openRPCTestDB(t), runner)
	rsp, err := svc.GetEngineStatus(context.Background(), &factorpb.GetEngineStatusReq{})
	require.NoError(t, err)
	require.EqualValues(t, 3, rsp.GetQueueDepth())
	require.EqualValues(t, 4, rsp.GetQueueOverflowCount())
}

type fakeRPCScheduler struct {
	status scheduler.Status
	tasks  []scheduler.Task
	err    error
}

func (f *fakeRPCScheduler) Status() scheduler.Status { return f.status }
func (f *fakeRPCScheduler) Run(_ context.Context, task scheduler.Task) error {
	f.tasks = append(f.tasks, task)
	return f.err
}

func openRPCTestDB(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "factor.db")})
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(factorschema.AllSQL()))
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func seedRPCFactorAndBinding(t *testing.T, db *store.Store, status string) {
	t.Helper()
	require.NoError(t, db.Factors().Create(context.Background(), domain.FactorDef{
		FactorID: "bias", Name: "Bias", SourceCode: "x", SourceHash: "hash",
		InputColumns: []string{"close"}, Outputs: []string{"bias"}, ParamsJSON: `{}`,
		LookbackRows: 20, Status: status,
	}))
	require.NoError(t, db.Bindings().Upsert(context.Background(), domain.FactorBinding{
		BindingID: "bind", FactorID: "bias", SpaceID: "crypto", SourceDataset: "bars",
		Freq: "1m", SubjectMode: domain.SubjectModeAll, SubjectsJSON: "[]",
		TargetDataset: "bars_factor", Status: domain.BindingStatusEnabled,
	}))
}

func genericFactorPB(id, name string, outputs []string) *factorpb.FactorDef {
	return &factorpb.FactorDef{
		FactorId: id, Name: name, SourceCode: "def compute(df, params): return {}",
		InputColumns: []string{"close"}, Outputs: outputs, ParamsJson: `{}`,
		LookbackRows: 20, Status: domain.FactorStatusEnabled,
	}
}
