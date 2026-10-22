package pipeline

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/stretchr/testify/require"
)

// memoryStorage is an in-memory Storage that keeps the same idempotency
// contract as WriteFactorRows: replaying a commit_id with identical rows is a
// no-op, replaying it with different rows is a conflict.
type memoryStorage struct {
	mu      sync.Mutex
	source  map[string][]time.Time
	closes  map[string]float64
	commits map[string][]storageio.ResultRow
	result  map[string]storageio.ResultRow
	markers []storageio.PeriodMarker
	// commitDatasets records which result dataset each commit was written to.
	commitDatasets map[string]string
	reads          int
}

func newMemoryStorage(subjects []string, target time.Time, periods int) *memoryStorage {
	store := &memoryStorage{
		source: map[string][]time.Time{}, closes: map[string]float64{},
		commits: map[string][]storageio.ResultRow{}, result: map[string]storageio.ResultRow{},
		commitDatasets: map[string]string{},
	}
	for index, subject := range subjects {
		for offset := periods - 1; offset >= 0; offset-- {
			ts := target.Add(-time.Duration(offset) * time.Minute)
			store.source[subject] = append(store.source[subject], ts)
			store.closes[subject+ts.Format(time.RFC3339)] = float64(100*(index+1) + (periods - offset))
		}
	}
	return store
}

// data 把内存里的源数据适配成管线的数据层（每次 Load 计一次读取）。
func (s *memoryStorage) data() Data {
	return &frameData{read: func(ctx context.Context, plan Plan, start time.Time) (map[string]*storageio.Frame, error) {
		return s.ReadWindow(ctx, storageio.ReadRequest{Subjects: plan.Available, Start: start, End: plan.TargetEnd, Columns: plan.CarryColumns})
	}}
}

func (*memoryStorage) DatasetColumns(context.Context, string, string) ([]string, error) {
	return nil, nil
}

func (s *memoryStorage) ReadWindow(_ context.Context, req storageio.ReadRequest) (map[string]*storageio.Frame, error) {
	s.mu.Lock()
	s.reads++
	s.mu.Unlock()
	frames := make(map[string]*storageio.Frame, len(req.Subjects))
	for _, subject := range req.Subjects {
		frame := &storageio.Frame{SubjectID: subject, Columns: append([]string{"data_time", "series_tag"}, req.Columns...)}
		for _, ts := range s.source[subject] {
			if ts.Before(req.Start) || !ts.Before(req.End) {
				continue
			}
			frame.Rows = append(frame.Rows, []any{ts, "venue:binance", s.closes[subject+ts.Format(time.RFC3339)]})
		}
		frames[subject] = frame
	}
	return frames, nil
}

func (s *memoryStorage) WriteRows(_ context.Context, _, datasetID, commitID string, rows []storageio.ResultRow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commitDatasets[commitID] = datasetID
	if previous, ok := s.commits[commitID]; ok {
		if len(previous) != len(rows) {
			return errCommitConflict
		}
		return nil
	}
	s.commits[commitID] = append([]storageio.ResultRow(nil), rows...)
	for _, row := range rows {
		s.result[row.SubjectID+row.DataTime.Format(time.RFC3339)+row.SeriesTag] = row
	}
	return nil
}

func (s *memoryStorage) ReportComputed(_ context.Context, marker storageio.PeriodMarker) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.markers = append(s.markers, marker)
	return nil
}

func (*memoryStorage) ComputedExists(context.Context, string, string, string, int64) (bool, error) {
	return false, nil
}

var errCommitConflict = errors.New("commit_id replayed with different rows")

// e2eMean 是三期均值；e2eBroken 模拟 mgr 与引擎 expr 版本不一致：派生字段来自 mgr，引擎编译不了 spec。
func e2eMean() domain.FactorDef {
	return domain.MustDefinition("mean", "mean", "outputs:\n  mean3: ts_mean(close, 3)\n")
}

func e2eBroken() domain.FactorDef {
	return domain.FactorDef{FactorID: "broken", Name: "broken", Spec: "outputs:\n  broken_out: newer_function(close)\n",
		FactorType: domain.FactorTypeTimeSeries, InputColumns: []string{"close"}, Outputs: []string{"broken_out"}, LookbackPeriods: 3}
}

func e2eLiveInput(target time.Time, subjects []string, factors []domain.FactorDef) LiveInput {
	return LiveInput{
		Set: domain.FactorSet{
			SetID: "set_e2e", SpaceID: "crypto", SourceDatasetID: "dataset_bars", Freq: "1m",
			SubjectMode: domain.SubjectModeAll, ResultDatasetID: "dataset_factor_bars_1m", Status: domain.SetStatusEnabled,
		},
		Factors: factors, PeriodTime: target, Universe: subjects, CarryColumns: []string{"close"}, TriggerEventID: "evt-1",
	}
}

func sortedRows(store *memoryStorage) []storageio.ResultRow {
	rows := make([]storageio.ResultRow, 0, len(store.result))
	for _, row := range store.result {
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].SubjectID < rows[j].SubjectID })
	return rows
}

// TestLivePeriodEndToEnd drives plan -> load -> expression compute -> assemble
// -> write -> report for one live period against in-memory Storage.
func TestLivePeriodEndToEnd(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	subjects := []string{"BTC", "ETH", "SOL"}
	mean, broken := e2eMean(), e2eBroken()
	clock := periodclock.Continuous{}

	t.Run("writes result rows and complete marker", func(t *testing.T) {
		store := newMemoryStorage(subjects, target, 10)
		runner := NewRunner(store, store.data(), clock, Config{ComputeWorkers: 2})
		plan, err := BuildLivePlan(clock, e2eLiveInput(target, subjects, []domain.FactorDef{mean}))
		require.NoError(t, err)

		outcome, err := runner.Run(context.Background(), plan)
		require.NoError(t, err)
		require.Equal(t, "complete", outcome.Status)
		require.Equal(t, 3, outcome.RowsWritten)

		rows := sortedRows(store)
		require.Len(t, rows, 3)
		for index, row := range rows {
			closes := []float64{
				float64(100*(index+1) + 8), float64(100*(index+1) + 9), float64(100*(index+1) + 10),
			}
			require.Equal(t, target, row.DataTime)
			require.InDelta(t, closes[2], row.Fields["close"], 1e-9)
			require.InDelta(t, (closes[0]+closes[1]+closes[2])/3, row.Fields["mean3"], 1e-9)
		}
		require.Len(t, store.markers, 1)
		marker := store.markers[0]
		require.Equal(t, target.Unix(), marker.PeriodTime)
		require.Equal(t, "dataset_factor_bars_1m", marker.ResultDatasetID)
		require.Equal(t, "complete", marker.Status)
		require.Empty(t, marker.FailedSubjects)
		require.Equal(t, []string{"BTC", "ETH", "SOL"}, marker.UniverseSubjects)
		require.Len(t, marker.Factors, 1)
		require.Equal(t, "complete", marker.Factors[0].Status)
	})

	t.Run("duplicate delivery is idempotent", func(t *testing.T) {
		store := newMemoryStorage(subjects, target, 10)
		runner := NewRunner(store, store.data(), clock, Config{ComputeWorkers: 2})
		plan, err := BuildLivePlan(clock, e2eLiveInput(target, subjects, []domain.FactorDef{mean}))
		require.NoError(t, err)

		_, err = runner.Run(context.Background(), plan)
		require.NoError(t, err)
		_, err = runner.Run(context.Background(), plan)
		require.NoError(t, err)

		require.Len(t, store.commits, 1, "replays must reuse the deterministic commit_id")
		require.Len(t, store.result, 3)
	})

	t.Run("upstream failed subject degrades the period", func(t *testing.T) {
		store := newMemoryStorage(subjects, target, 10)
		runner := NewRunner(store, store.data(), clock, Config{ComputeWorkers: 2})
		input := e2eLiveInput(target, subjects, []domain.FactorDef{mean})
		input.UpstreamFailed = []string{"SOL"}
		plan, err := BuildLivePlan(clock, input)
		require.NoError(t, err)

		outcome, err := runner.Run(context.Background(), plan)
		require.NoError(t, err)
		require.Equal(t, "degraded", outcome.Status)
		require.Equal(t, []string{"SOL"}, outcome.FailedSubjects)
		require.Len(t, store.result, 2)
		require.Equal(t, "degraded", store.markers[0].Status)
		require.Equal(t, []string{"SOL"}, store.markers[0].FailedSubjects)
	})

	t.Run("failing factor writes null column and degrades", func(t *testing.T) {
		store := newMemoryStorage(subjects, target, 10)
		runner := NewRunner(store, store.data(), clock, Config{ComputeWorkers: 2})
		plan, err := BuildLivePlan(clock, e2eLiveInput(target, subjects, []domain.FactorDef{broken, mean}))
		require.NoError(t, err)

		outcome, err := runner.Run(context.Background(), plan)
		require.NoError(t, err)
		require.Equal(t, "degraded", outcome.Status)
		states := map[string]string{}
		for _, state := range store.markers[0].Factors {
			states[state.FactorID] = state.Status
		}
		require.Equal(t, "complete", states["mean"])
		require.NotEqual(t, "complete", states["broken"])
		for _, row := range store.result {
			require.Contains(t, row.Fields, "broken_out")
			require.Nil(t, row.Fields["broken_out"])
			require.NotNil(t, row.Fields["mean3"])
		}
	})

	t.Run("no enabled member still reports the period", func(t *testing.T) {
		store := newMemoryStorage(subjects, target, 10)
		runner := NewRunner(store, store.data(), clock, Config{ComputeWorkers: 2})
		plan, err := BuildLivePlan(clock, e2eLiveInput(target, subjects, []domain.FactorDef{}))
		require.NoError(t, err)

		outcome, err := runner.Run(context.Background(), plan)
		require.NoError(t, err)
		require.Equal(t, "complete", outcome.Status)
		require.Empty(t, store.result, "a set without enabled members must not compute or write rows")
		require.Len(t, store.markers, 1)
	})
}

// TestSameDefinitionInTwoSetsComputesIntoEachResultDataset verifies that one
// definition used by two sets is written to each set's own result dataset with
// its own commit id and marker.
func TestSameDefinitionInTwoSetsComputesIntoEachResultDataset(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	subjects := []string{"BTC", "ETH"}
	mean := e2eMean()
	clock := periodclock.Continuous{}
	store := newMemoryStorage(subjects, target, 10)
	runner := NewRunner(store, store.data(), clock, Config{ComputeWorkers: 2})

	for _, set := range []domain.FactorSet{
		{SetID: "set_1m", ResultDatasetID: "dataset_factor_bars_1m"},
		{SetID: "set_other", ResultDatasetID: "dataset_factor_bars_other"},
	} {
		input := e2eLiveInput(target, subjects, []domain.FactorDef{mean})
		input.Set.SetID, input.Set.ResultDatasetID = set.SetID, set.ResultDatasetID
		plan, err := BuildLivePlan(clock, input)
		require.NoError(t, err)
		outcome, err := runner.Run(context.Background(), plan)
		require.NoError(t, err)
		require.Equal(t, "complete", outcome.Status)
	}

	require.Len(t, store.commitDatasets, 2, "each set owns a distinct commit id")
	datasets := make([]string, 0, 2)
	for _, datasetID := range store.commitDatasets {
		datasets = append(datasets, datasetID)
	}
	require.ElementsMatch(t, []string{"dataset_factor_bars_1m", "dataset_factor_bars_other"}, datasets)
	require.Len(t, store.markers, 2)
	require.NotEqual(t, store.markers[0].ResultDatasetID, store.markers[1].ResultDatasetID)
}
