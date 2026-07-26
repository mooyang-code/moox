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

func TestCreateFactorUsesPeriodsAndExtractedDepends(t *testing.T) {
	svc := NewWithRuntime(openRPCTestDB(t), nil, WithFactorsDir(t.TempDir()))
	rsp, err := svc.CreateFactor(context.Background(), &factorpb.CreateFactorReq{Factor: &factorpb.FactorDef{
		FactorId: "bias", Name: "Bias",
		SourceCode: "extra_data_dict={'x':['funding_rate']}\ndef signal(*args): return args[0]\n",
		Periods:    []int32{20}, LookbackBars: 20, Status: domain.FactorStatusEnabled,
	}})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, []string{"funding_rate"}, rsp.GetFactor().GetDepends())
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
	require.NoError(t, db.Factors().Upsert(context.Background(), domain.FactorDef{
		FactorID: "bias", Name: "Bias", SourceCode: "x", SourceHash: "hash",
		Periods: []int{20}, LookbackBars: 20, Status: status,
	}))
	require.NoError(t, db.Bindings().Upsert(context.Background(), domain.FactorBinding{
		BindingID: "bind", FactorID: "bias", SpaceID: "crypto", SourceDataset: "bars",
		Freq: "1m", SubjectMode: domain.SubjectModeAll, SubjectsJSON: "[]",
		TargetDataset: "bars_factor", Status: domain.BindingStatusEnabled,
	}))
}
