package pipeline

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pyexec"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/stretchr/testify/require"
)

func TestAssembleKeepsOnlyTargetRange(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	factor := testPipelineFactor("momentum", domain.FactorTypeTimeSeries, 2, false)
	plan := computePlan(target, []domain.FactorDef{factor}, []string{"BTC"})
	loaded := framesFor([]string{"BTC"}, target)
	loaded.Frames["BTC"].Rows = append(loaded.Frames["BTC"].Rows, []any{target.Add(-time.Minute), "venue:binance", int64(9)})
	computation := successfulComputation(factor, []string{"data_time", "series_tag", factor.Outputs[0]}, [][]any{
		{target.Add(-time.Minute), "venue:binance", 11.0}, {target, "venue:binance", 12.0},
	})
	rows, err := NewRunner(&outputStore{}, nil, periodclock.Continuous{}, Config{}).Assemble(plan, loaded, computation)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, target, rows[0].DataTime)
}

func TestAssembleCarriesSourceColumnsOnSourceTagRows(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	factor := testPipelineFactor("momentum", domain.FactorTypeTimeSeries, 2, false)
	plan := computePlan(target, []domain.FactorDef{factor}, []string{"BTC"})
	plan.CarryColumns = append(plan.CarryColumns, "volume")
	loaded := framesFor([]string{"BTC"}, target)
	loaded.Frames["BTC"].Columns = append(loaded.Frames["BTC"].Columns, "volume")
	loaded.Frames["BTC"].Rows[0] = append(loaded.Frames["BTC"].Rows[0], int64(42))
	computation := successfulComputation(factor, []string{"data_time", "series_tag", factor.Outputs[0]}, [][]any{{target, "venue:binance", 12.0}})
	rows, err := NewRunner(&outputStore{}, nil, periodclock.Continuous{}, Config{}).Assemble(plan, loaded, computation)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"close": 10.0, "volume": int64(42), factor.Outputs[0]: 12.0}, rows[0].Fields)
}

func TestAssembleDerivedTagWritesFactorColumnsOnly(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	factor := testPipelineFactor("spread", domain.FactorTypeCrossSection, 2, true)
	factor.Outputs = []string{"spread_value"}
	plan := computePlan(target, []domain.FactorDef{factor}, []string{"BTC"})
	loaded := framesFor([]string{"BTC"}, target)
	computation := Computation{
		Results:      map[string]map[string]pyexec.ItemResult{factor.FactorID: {"": {FactorID: factor.FactorID, Columns: []string{"data_time", "series_tag", "subject_id", "spread_value"}, Rows: [][]any{{target, "binance-okx", "BTC", 1.5}}}}},
		FactorStates: map[string]storageio.FactorState{factor.FactorID: {FactorID: factor.FactorID, Status: "complete", SourceHash: factor.SourceHash}},
	}
	rows, err := NewRunner(&outputStore{}, nil, periodclock.Continuous{}, Config{}).Assemble(plan, loaded, computation)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	var derived storageio.ResultRow
	for _, row := range rows {
		if row.SeriesTag == "binance-okx" {
			derived = row
		}
	}
	require.Equal(t, map[string]any{"spread_value": 1.5}, derived.Fields)
}

func TestAssembleFailedFactorWritesNull(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	factor := testPipelineFactor("momentum", domain.FactorTypeTimeSeries, 2, false)
	plan := computePlan(target, []domain.FactorDef{factor}, []string{"BTC"})
	loaded := framesFor([]string{"BTC"}, target)
	computation := Computation{Results: map[string]map[string]pyexec.ItemResult{factor.FactorID: {"BTC": {FactorID: factor.FactorID, Err: errors.New("compute failed")}}}, FactorStates: map[string]storageio.FactorState{factor.FactorID: {FactorID: factor.FactorID, Status: "degraded"}}}
	rows, err := NewRunner(&outputStore{}, nil, periodclock.Continuous{}, Config{}).Assemble(plan, loaded, computation)
	require.NoError(t, err)
	require.Contains(t, rows[0].Fields, factor.Outputs[0])
	require.Nil(t, rows[0].Fields[factor.Outputs[0]])
}

func TestAssembleNaNAndInfBecomeNull(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	factor := testPipelineFactor("momentum", domain.FactorTypeTimeSeries, 2, false)
	plan := computePlan(target, []domain.FactorDef{factor}, []string{"BTC"})
	loaded := framesFor([]string{"BTC"}, target)
	computation := successfulComputation(factor, []string{"data_time", "series_tag", factor.Outputs[0]}, [][]any{{target, "venue:binance", math.NaN()}})
	rows, err := NewRunner(&outputStore{}, nil, periodclock.Continuous{}, Config{}).Assemble(plan, loaded, computation)
	require.NoError(t, err)
	require.Nil(t, rows[0].Fields[factor.Outputs[0]])
}

func TestAssembleRecalcWritesOnlySelectedFactors(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	selected := testPipelineFactor("selected", domain.FactorTypeTimeSeries, 2, false)
	unselected := testPipelineFactor("unselected", domain.FactorTypeTimeSeries, 2, false)
	plan := computePlan(target, []domain.FactorDef{selected}, []string{"BTC"})
	plan.Mode = ModeRecalc
	loaded := framesFor([]string{"BTC"}, target)
	computation := successfulComputation(selected, []string{"data_time", "series_tag", selected.Outputs[0]}, [][]any{{target, "venue:binance", 1.0}})
	computation.FactorStates[unselected.FactorID] = storageio.FactorState{FactorID: unselected.FactorID, Status: "complete"}
	rows, err := NewRunner(&outputStore{}, nil, periodclock.Continuous{}, Config{}).Assemble(plan, loaded, computation)
	require.NoError(t, err)
	require.NotContains(t, rows[0].Fields, unselected.Outputs[0])
}

func TestWriteBatchesRows(t *testing.T) {
	store := &outputStore{}
	rows := make([]storageio.ResultRow, 2500)
	for i := range rows {
		rows[i] = storageio.ResultRow{SubjectID: "BTC", DataTime: time.Unix(int64(i+1), 0).UTC(), Fields: map[string]any{"close": i}}
	}
	runner := NewRunner(store, nil, periodclock.Continuous{}, Config{})
	require.NoError(t, runner.Write(context.Background(), Plan{Set: domain.FactorSet{SetID: "set", SpaceID: "crypto", ResultDatasetID: "dataset_factor_bars"}}, time.Unix(10000, 0), rows, 1000))
	require.Len(t, store.writeCalls, 3)
	require.NotEqual(t, store.writeCalls[0].commitID, store.writeCalls[1].commitID)
	require.NotEqual(t, store.writeCalls[1].commitID, store.writeCalls[2].commitID)
	require.Equal(t, store.writeCalls[0].commitID, storageio.CommitID("set", 10000, rows[:1000]))
}

func TestWriteRetriesTransientThenReturnsErrInfra(t *testing.T) {
	store := &outputStore{writeErr: fmt.Errorf("%w: write unavailable", storageio.ErrInfra)}
	runner := NewRunner(store, nil, periodclock.Continuous{}, Config{})
	err := runner.Write(context.Background(), Plan{Set: domain.FactorSet{SetID: "set", SpaceID: "crypto", ResultDatasetID: "dataset_factor_bars"}}, time.Unix(10000, 0), []storageio.ResultRow{{SubjectID: "BTC", DataTime: time.Unix(10000, 0), Fields: map[string]any{"close": 1}}}, 1000)
	require.ErrorIs(t, err, storageio.ErrInfra)
	require.Len(t, store.writeCalls, 3)
}

func TestWritePermanentErrorIsNotRetriedNorInfra(t *testing.T) {
	store := &outputStore{writeErr: errors.New("column circulating_supply is not defined")}
	runner := NewRunner(store, nil, periodclock.Continuous{}, Config{})
	err := runner.Write(context.Background(), Plan{Set: domain.FactorSet{SetID: "set", SpaceID: "crypto", ResultDatasetID: "dataset_factor_bars"}}, time.Unix(10000, 0), []storageio.ResultRow{{SubjectID: "BTC", DataTime: time.Unix(10000, 0), Fields: map[string]any{"close": 1}}}, 1000)
	require.Error(t, err)
	require.False(t, errors.Is(err, storageio.ErrInfra))
	require.Len(t, store.writeCalls, 1)
}

func TestOutcomeStatusDegradedWhenAnyFactorFailed(t *testing.T) {
	factor := domain.FactorDef{FactorID: "bad"}
	outcome := outcomeFor(Plan{Factors: []domain.FactorDef{factor}}, LoadResult{}, Computation{FactorStates: map[string]storageio.FactorState{"bad": {FactorID: "bad", Status: "degraded"}}})
	require.Equal(t, "degraded", outcome.Status)
}

func TestReportOnlyInLiveMode(t *testing.T) {
	store := &outputStore{}
	runner := NewRunner(store, nil, periodclock.Continuous{}, Config{})
	plan := Plan{Mode: ModeRecalc, Set: domain.FactorSet{SpaceID: "crypto", ResultDatasetID: "dataset_factor_bars", SourceDatasetID: "dataset_bars", Freq: "1m"}}
	require.NoError(t, runner.Report(context.Background(), plan, Outcome{Status: "complete"}))
	require.Empty(t, store.markers)
	plan.Mode = ModeLive
	plan.TargetEnd = time.Date(2026, 10, 4, 0, 11, 0, 0, time.UTC)
	plan.TriggerEventID = "event-1"
	require.NoError(t, runner.Report(context.Background(), plan, Outcome{Status: "complete"}))
	require.Len(t, store.markers, 1)
}

func TestNoEnabledFactorsStillReportsComplete(t *testing.T) {
	store := &outputStore{}
	runner := NewRunner(store, nil, periodclock.Continuous{}, Config{})
	plan := Plan{Mode: ModeLive, Set: domain.FactorSet{SpaceID: "crypto", SourceDatasetID: "dataset_bars", ResultDatasetID: "dataset_factor_bars", Freq: "1m"}, TargetEnd: time.Date(2026, 10, 4, 0, 11, 0, 0, time.UTC), TriggerEventID: "event-1", Expected: []string{"BTC"}}
	outcome, err := runner.Run(context.Background(), plan)
	require.NoError(t, err)
	require.Equal(t, "complete", outcome.Status)
	require.Empty(t, outcome.Factors)
	require.Len(t, store.markers, 1)
	require.Empty(t, store.writeCalls)
}

func TestNoEnabledFactorsWithUpstreamFailureReportsDegraded(t *testing.T) {
	store := &outputStore{}
	runner := NewRunner(store, nil, periodclock.Continuous{}, Config{})
	plan := Plan{Mode: ModeLive, Set: domain.FactorSet{SpaceID: "crypto", SourceDatasetID: "dataset_bars", ResultDatasetID: "dataset_factor_bars", Freq: "1m"}, TargetEnd: time.Date(2026, 10, 4, 0, 11, 0, 0, time.UTC), TriggerEventID: "event-1", Expected: []string{"BTC", "ETH"}, UpstreamFailed: []string{"ETH"}}
	outcome, err := runner.Run(context.Background(), plan)
	require.NoError(t, err)
	require.Equal(t, "degraded", outcome.Status)
	require.Equal(t, []string{"ETH"}, outcome.FailedSubjects)
	require.Len(t, store.markers, 1)
	require.Equal(t, "degraded", store.markers[0].Status)
	require.Equal(t, []string{"ETH"}, store.markers[0].FailedSubjects)
}

func successfulComputation(factor domain.FactorDef, columns []string, rows [][]any) Computation {
	return Computation{
		Results:      map[string]map[string]pyexec.ItemResult{factor.FactorID: {"BTC": {FactorID: factor.FactorID, Columns: columns, Rows: rows}}},
		FactorStates: map[string]storageio.FactorState{factor.FactorID: {FactorID: factor.FactorID, Status: "complete", SourceHash: factor.SourceHash}},
	}
}

type outputStore struct {
	writeErr   error
	writeCalls []struct {
		spaceID, datasetID, commitID string
		rows                         []storageio.ResultRow
	}
	markers []storageio.PeriodMarker
}

func (*outputStore) DatasetColumns(context.Context, string, string) ([]string, error) {
	return nil, nil
}
func (*outputStore) ReadWindow(context.Context, storageio.ReadRequest) (map[string]*storageio.Frame, error) {
	return nil, nil
}
func (s *outputStore) WriteRows(_ context.Context, spaceID, datasetID, commitID string, rows []storageio.ResultRow) error {
	s.writeCalls = append(s.writeCalls, struct {
		spaceID, datasetID, commitID string
		rows                         []storageio.ResultRow
	}{spaceID, datasetID, commitID, append([]storageio.ResultRow(nil), rows...)})
	return s.writeErr
}
func (s *outputStore) ReportComputed(_ context.Context, marker storageio.PeriodMarker) error {
	s.markers = append(s.markers, marker)
	return nil
}
func (*outputStore) ComputedExists(context.Context, string, string, string, int64) (bool, error) {
	return false, nil
}
