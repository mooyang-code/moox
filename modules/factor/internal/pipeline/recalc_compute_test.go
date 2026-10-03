package pipeline

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pyexec"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/stretchr/testify/require"
)

func TestComputeRecalcTimeSeriesOneCallPerSubjectForWholeChunk(t *testing.T) {
	start := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	factor := testPipelineFactor("momentum", domain.FactorTypeTimeSeries, 2, false)
	plan := recalcPlan(start, []domain.FactorDef{factor}, []string{"BTC", "ETH"})
	loaded := recalcFrames(plan.Available, start, 3)
	var calls atomic.Int32
	executor := &fakeExecutor{exec: func(_ context.Context, req pyexec.Request) ([]pyexec.ItemResult, error) {
		calls.Add(1)
		contextValues := req.Context
		if targets, ok := contextValues["target_period_times"].([]string); !ok || len(targets) != 3 {
			t.Errorf("expected three target periods, got %#v", contextValues["target_period_times"])
		}
		frame := req.Frame.(storageio.Frame)
		if len(frame.Rows) != 4 {
			t.Errorf("expected lookback plus three output periods, got %d rows", len(frame.Rows))
		}
		return []pyexec.ItemResult{{FactorID: factor.FactorID, Columns: []string{"data_time", "series_tag", factor.Outputs[0]}, Rows: [][]any{
			{start, "venue:binance", 1.0}, {start.Add(time.Minute), "venue:binance", 2.0}, {start.Add(2 * time.Minute), "venue:binance", 3.0},
		}}}, nil
	}}
	computation, err := NewRunner(&fakeStore{}, executor, periodclock.Continuous{}, Config{PythonWorkers: 2}).Compute(context.Background(), plan, loaded)
	require.NoError(t, err)
	require.EqualValues(t, 2, calls.Load())
	require.Len(t, computation.Results[factor.FactorID]["BTC"].Rows, 3)
}

func TestComputeRecalcCrossSectionRunsOncePerPeriod(t *testing.T) {
	start := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	factor := testPipelineFactor("spread", domain.FactorTypeCrossSection, 2, true)
	factor.Outputs = []string{"spread_value"}
	plan := recalcPlan(start, []domain.FactorDef{factor}, []string{"BTC", "ETH"})
	loaded := recalcFrames(plan.Available, start, 3)
	var calls atomic.Int32
	executor := &fakeExecutor{exec: func(_ context.Context, req pyexec.Request) ([]pyexec.ItemResult, error) {
		calls.Add(1)
		periodTime := req.Context["period_time"].(int64)
		frame := req.Frame.(storageio.Frame)
		if len(frame.Rows) != 4 {
			t.Errorf("cross section should receive one period window, got %d rows", len(frame.Rows))
		}
		at := time.Unix(periodTime, 0).UTC()
		return []pyexec.ItemResult{{FactorID: factor.FactorID, Columns: []string{"data_time", "series_tag", "subject_id", "spread_value"}, Rows: [][]any{
			{at, "derived", "BTC", 1.0}, {at, "derived", "ETH", 2.0},
		}}}, nil
	}}
	computation, err := NewRunner(&fakeStore{}, executor, periodclock.Continuous{}, Config{PythonWorkers: 3}).Compute(context.Background(), plan, loaded)
	require.NoError(t, err)
	require.EqualValues(t, 3, calls.Load())
	require.Len(t, computation.Results[factor.FactorID][""].Rows, 6)
}

func recalcPlan(start time.Time, factors []domain.FactorDef, subjects []string) Plan {
	return Plan{
		Mode: ModeRecalc, Set: domain.FactorSet{SpaceID: "crypto", SourceDatasetID: "dataset_bars", ResultDatasetID: "dataset_factor_bars", Freq: "1m"},
		Factors: factors, TargetStart: start, TargetEnd: start.Add(3 * time.Minute),
		Expected: append([]string(nil), subjects...), Available: append([]string(nil), subjects...), CarryColumns: []string{"close"}, WriteCarry: true,
	}
}

func recalcFrames(subjects []string, start time.Time, targetPeriods int) LoadResult {
	frames := make(map[string]*storageio.Frame, len(subjects))
	for _, subject := range subjects {
		frame := &storageio.Frame{SubjectID: subject, Columns: []string{"data_time", "series_tag", "close"}}
		for index := -1; index < targetPeriods; index++ {
			frame.Rows = append(frame.Rows, []any{start.Add(time.Duration(index) * time.Minute), "venue:binance", float64(index + 2)})
		}
		frames[subject] = frame
	}
	return LoadResult{Frames: frames, Available: append([]string(nil), subjects...)}
}
