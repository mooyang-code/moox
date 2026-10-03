package pipeline

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pyexec"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/stretchr/testify/require"
)

func TestComputeTimeSeriesOneCallPerSubject(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	factors := make([]domain.FactorDef, 4)
	for i := range factors {
		factors[i] = testPipelineFactor("factor_"+string(rune('a'+i)), domain.FactorTypeTimeSeries, 3, false)
	}
	executor := &fakeExecutor{exec: func(_ context.Context, req pyexec.Request) ([]pyexec.ItemResult, error) {
		frame, ok := req.Frame.(storageio.Frame)
		if !ok || len(req.Factors) != 4 || req.Context["subject_id"] != frame.SubjectID {
			t.Errorf("unexpected timeseries request: frame=%T factors=%d context=%v", req.Frame, len(req.Factors), req.Context)
		}
		return successfulItems(req.Factors), nil
	}}
	plan := computePlan(target, factors, []string{"BTC", "ETH", "SOL"})
	loaded := framesFor(plan.Available, target)
	computation, err := NewRunner(&fakeStore{}, executor, periodclock.Continuous{}, Config{PythonWorkers: 2}).Compute(context.Background(), plan, loaded)
	require.NoError(t, err)
	require.Len(t, executor.requests(), 3)
	for _, factor := range factors {
		require.Equal(t, "complete", computation.FactorStates[factor.FactorID].Status)
		require.Len(t, computation.Results[factor.FactorID], 3)
	}
}

func TestComputeCrossSectionSkipsWhenPartialUniverseNotAllowed(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	factor := testPipelineFactor("spread", domain.FactorTypeCrossSection, 2, false)
	executor := &fakeExecutor{}
	plan := computePlan(target, []domain.FactorDef{factor}, []string{"BTC", "ETH"})
	loaded := framesFor([]string{"BTC"}, target)
	loaded.Available = []string{"BTC"}
	loaded.FailedSubjects = []string{"ETH"}
	computation, err := NewRunner(&fakeStore{}, executor, periodclock.Continuous{}, Config{}).Compute(context.Background(), plan, loaded)
	require.NoError(t, err)
	require.Empty(t, executor.requests())
	require.Equal(t, "skipped", computation.FactorStates[factor.FactorID].Status)
}

func TestComputeCrossSectionRunsOnAvailableWhenAllowed(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	factor := testPipelineFactor("spread", domain.FactorTypeCrossSection, 2, true)
	executor := &fakeExecutor{exec: func(_ context.Context, req pyexec.Request) ([]pyexec.ItemResult, error) {
		frame, ok := req.Frame.(storageio.Frame)
		if !ok || len(frame.Rows) != 1 || len(frame.Columns) != 4 || frame.Columns[2] != "subject_id" || frame.Rows[0][2] != "BTC" {
			t.Errorf("unexpected cross-section panel: %#v", req.Frame)
		}
		return successfulItems(req.Factors), nil
	}}
	plan := computePlan(target, []domain.FactorDef{factor}, []string{"BTC", "ETH"})
	loaded := framesFor([]string{"BTC"}, target)
	loaded.Available = []string{"BTC"}
	loaded.FailedSubjects = []string{"ETH"}
	computation, err := NewRunner(&fakeStore{}, executor, periodclock.Continuous{}, Config{}).Compute(context.Background(), plan, loaded)
	require.NoError(t, err)
	require.Len(t, executor.requests(), 1)
	require.Equal(t, "degraded", computation.FactorStates[factor.FactorID].Status)
	require.Equal(t, []string{"ETH"}, computation.FactorStates[factor.FactorID].FailedSubjects)
}

func TestComputeBudgetExceededCancelsRemaining(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	factor := testPipelineFactor("momentum", domain.FactorTypeTimeSeries, 2, false)
	var calls atomic.Int32
	executor := &fakeExecutor{exec: func(ctx context.Context, req pyexec.Request) ([]pyexec.ItemResult, error) {
		calls.Add(1)
		frame := req.Frame.(storageio.Frame)
		if frame.SubjectID == "B" {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return successfulItems(req.Factors), nil
	}}
	plan := computePlan(target, []domain.FactorDef{factor}, []string{"A", "B", "C"})
	plan.Budget = 20 * time.Millisecond
	loaded := framesFor(plan.Available, target)
	computation, err := NewRunner(&fakeStore{}, executor, periodclock.Continuous{}, Config{PythonWorkers: 1}).Compute(context.Background(), plan, loaded)
	require.NoError(t, err)
	require.True(t, computation.BudgetHit)
	require.EqualValues(t, 2, calls.Load())
	require.Empty(t, computation.Results[factor.FactorID]["A"].Err)
	require.Error(t, computation.Results[factor.FactorID]["B"].Err)
	require.Error(t, computation.Results[factor.FactorID]["C"].Err)
}

func testPipelineFactor(id, factorType string, lookback int, partial bool) domain.FactorDef {
	code := "def compute(df, params, context): return df"
	return domain.FactorDef{
		FactorID: id, Name: id, FactorType: factorType, SourceCode: code, SourceHash: domain.SourceHash(code),
		InputColumns: []string{"close"}, Outputs: []string{id + "_value"}, ParamsJSON: "{}",
		LookbackPeriods: lookback, AllowPartialUniverse: partial, Status: domain.FactorStatusEnabled,
	}
}

func computePlan(target time.Time, factors []domain.FactorDef, subjects []string) Plan {
	return Plan{
		Mode: ModeLive, Set: domain.FactorSet{SpaceID: "crypto", SourceDatasetID: "dataset_bars", Freq: "1m"},
		Factors: factors, TargetStart: target.Add(-time.Minute), TargetEnd: target.Add(time.Minute),
		Expected: append([]string(nil), subjects...), Available: append([]string(nil), subjects...), CarryColumns: []string{"close"},
	}
}

func framesFor(subjects []string, target time.Time) LoadResult {
	frames := make(map[string]*storageio.Frame, len(subjects))
	for _, subject := range subjects {
		frames[subject] = &storageio.Frame{
			SubjectID: subject, Columns: []string{"data_time", "series_tag", "close"},
			Rows: [][]any{{target, "venue:binance", 10.0}},
		}
	}
	return LoadResult{Frames: frames, Available: append([]string(nil), subjects...)}
}

func successfulItems(factors []pyexec.FactorCall) []pyexec.ItemResult {
	items := make([]pyexec.ItemResult, len(factors))
	for i, factor := range factors {
		items[i] = pyexec.ItemResult{FactorID: factor.FactorID, Columns: []string{"data_time", "series_tag", factor.Outputs[0]}, Rows: [][]any{}}
	}
	return items
}

type fakeExecutor struct {
	mu    sync.Mutex
	calls []pyexec.Request
	exec  func(context.Context, pyexec.Request) ([]pyexec.ItemResult, error)
}

func (f *fakeExecutor) Exec(ctx context.Context, req pyexec.Request) ([]pyexec.ItemResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	f.mu.Unlock()
	if f.exec != nil {
		return f.exec(ctx, req)
	}
	return successfulItems(req.Factors), nil
}

func (f *fakeExecutor) Busy() int    { return 0 }
func (f *fakeExecutor) Close() error { return nil }

func (f *fakeExecutor) requests() []pyexec.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pyexec.Request(nil), f.calls...)
}
