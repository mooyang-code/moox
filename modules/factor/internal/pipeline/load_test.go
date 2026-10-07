package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/stretchr/testify/require"
)

func TestLoadUsesMaxLookbackRange(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	plan, err := BuildLivePlan(periodclock.Continuous{}, LiveInput{
		Set: domain.FactorSet{SpaceID: "crypto", SourceDatasetID: "dataset_bars", Freq: "1m", Status: domain.SetStatusEnabled, SubjectMode: domain.SubjectModeAll},
		Factors: []domain.FactorDef{
			{FactorID: "short", LookbackPeriods: 5},
			{FactorID: "long", LookbackPeriods: 20},
		},
		PeriodTime: target, Universe: []string{"BTC"}, CarryColumns: []string{"close", "volume"},
	})
	require.NoError(t, err)
	store := &fakeStore{read: func(_ context.Context, req storageio.ReadRequest) (map[string]*storageio.Frame, error) {
		require.Equal(t, target.Add(-19*time.Minute), req.Start)
		require.Equal(t, target.Add(time.Minute), req.End)
		require.Equal(t, []string{"close", "volume"}, req.Columns)
		return emptyFrames(req), nil
	}}
	loaded, err := NewRunner(store, nil, periodclock.Continuous{}, Config{}).Load(context.Background(), plan)
	require.NoError(t, err)
	require.Equal(t, []string{"BTC"}, loaded.Available)
}

func TestLoadRecalcExtendsReadRangeByLookback(t *testing.T) {
	start := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	factors := []domain.FactorDef{
		{FactorID: "short", LookbackPeriods: 5},
		{FactorID: "long", LookbackPeriods: 20},
	}
	plan := Plan{Mode: ModeRecalc, Set: domain.FactorSet{SpaceID: "crypto", SourceDatasetID: "dataset_bars", Freq: "1m"}, Factors: factors,
		TargetStart: start, TargetEnd: start.Add(3 * time.Minute), Expected: []string{"BTC"}, Available: []string{"BTC"}, CarryColumns: []string{"close"}}
	store := &fakeStore{read: func(_ context.Context, req storageio.ReadRequest) (map[string]*storageio.Frame, error) {
		require.Equal(t, start.Add(-19*time.Minute), req.Start)
		require.Equal(t, start.Add(3*time.Minute), req.End)
		return emptyFrames(req), nil
	}}
	_, err := NewRunner(store, nil, periodclock.Continuous{}, Config{}).Load(context.Background(), plan)
	require.NoError(t, err)
}

func TestLoadBatchesSubjectsAndLimitsConcurrency(t *testing.T) {
	subjects := make([]string, 250)
	for i := range subjects {
		subjects[i] = fmt.Sprintf("S%03d", i)
	}
	var active, maxActive, calls atomic.Int32
	store := &fakeStore{read: func(_ context.Context, req storageio.ReadRequest) (map[string]*storageio.Frame, error) {
		calls.Add(1)
		current := active.Add(1)
		for previous := maxActive.Load(); current > previous && !maxActive.CompareAndSwap(previous, current); previous = maxActive.Load() {
		}
		time.Sleep(15 * time.Millisecond)
		active.Add(-1)
		return emptyFrames(req), nil
	}}
	plan := loadPlan(subjects)
	loaded, err := NewRunner(store, nil, periodclock.Continuous{}, Config{ReadBatchSubjects: 100, ReadWorkers: 2}).Load(context.Background(), plan)
	require.NoError(t, err)
	require.Equal(t, int32(3), calls.Load())
	require.LessOrEqual(t, maxActive.Load(), int32(2))
	require.Len(t, loaded.Frames, 250)
}

func TestLoadBatchFailureMarksSubjectsFailed(t *testing.T) {
	var mu sync.Mutex
	attempts := map[string]int{}
	store := &fakeStore{read: func(_ context.Context, req storageio.ReadRequest) (map[string]*storageio.Frame, error) {
		mu.Lock()
		defer mu.Unlock()
		first := req.Subjects[0]
		attempts[first]++
		if first == "A" {
			return nil, fmt.Errorf("%w: unavailable", storageio.ErrInfra)
		}
		return emptyFrames(req), nil
	}}
	loaded, err := NewRunner(store, nil, periodclock.Continuous{}, Config{ReadBatchSubjects: 2, ReadWorkers: 1, ReadRetries: 2}).Load(context.Background(), loadPlan([]string{"A", "B", "C", "D"}))
	require.NoError(t, err)
	require.Equal(t, []string{"A", "B"}, loaded.FailedSubjects)
	require.Equal(t, []string{"C", "D"}, loaded.Available)
	require.Equal(t, 3, attempts["A"])
	require.Len(t, loaded.Frames, 2)
}

func TestLoadAllBatchesFailReturnsErrInfra(t *testing.T) {
	store := &fakeStore{read: func(context.Context, storageio.ReadRequest) (map[string]*storageio.Frame, error) {
		return nil, errors.New("temporary network failure")
	}}
	_, err := NewRunner(store, nil, periodclock.Continuous{}, Config{ReadBatchSubjects: 1, ReadWorkers: 2, ReadRetries: 1}).Load(context.Background(), loadPlan([]string{"A", "B"}))
	require.ErrorIs(t, err, storageio.ErrInfra)
}

func loadPlan(subjects []string) Plan {
	return Plan{
		Mode: ModeLive, Set: domain.FactorSet{SpaceID: "crypto", SourceDatasetID: "dataset_bars", Freq: "1m"},
		TargetStart: time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC), TargetEnd: time.Date(2026, 10, 4, 0, 1, 0, 0, time.UTC),
		Expected: append([]string(nil), subjects...), Available: append([]string(nil), subjects...), CarryColumns: []string{"close"},
	}
}

func emptyFrames(req storageio.ReadRequest) map[string]*storageio.Frame {
	frames := make(map[string]*storageio.Frame, len(req.Subjects))
	for _, subject := range req.Subjects {
		frames[subject] = &storageio.Frame{SubjectID: subject, Columns: append([]string{"data_time", "series_tag"}, req.Columns...), Rows: [][]any{}}
	}
	return frames
}

type fakeStore struct {
	read         func(context.Context, storageio.ReadRequest) (map[string]*storageio.Frame, error)
	mu           sync.Mutex
	readRequests []storageio.ReadRequest
}

func (f *fakeStore) DatasetColumns(context.Context, string, string) ([]string, error) {
	return nil, nil
}

func (f *fakeStore) ReadWindow(ctx context.Context, req storageio.ReadRequest) (map[string]*storageio.Frame, error) {
	f.mu.Lock()
	f.readRequests = append(f.readRequests, req)
	f.mu.Unlock()
	return f.read(ctx, req)
}

func (*fakeStore) WriteRows(context.Context, string, string, string, []storageio.ResultRow) error {
	return nil
}
func (*fakeStore) ReportComputed(context.Context, storageio.PeriodMarker) error { return nil }
func (*fakeStore) ComputedExists(context.Context, string, string, string, int64) (bool, error) {
	return false, nil
}

func TestReadDeadlinesDifferForLiveAndRecalc(t *testing.T) {
	type observed struct {
		hasDeadline bool
		deadline    time.Duration
		pageTimeout time.Duration
	}
	got := map[Mode]observed{}
	for _, mode := range []Mode{ModeLive, ModeRecalc} {
		var seen observed
		store := &fakeStore{read: func(ctx context.Context, req storageio.ReadRequest) (map[string]*storageio.Frame, error) {
			deadline, ok := ctx.Deadline()
			seen = observed{hasDeadline: ok, deadline: time.Until(deadline), pageTimeout: req.PageTimeout}
			return emptyFrames(req), nil
		}}
		runner := NewRunner(store, nil, periodclock.Continuous{}, Config{ReadTimeout: 10 * time.Second})
		target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
		plan := computePlan(target, []domain.FactorDef{testPipelineFactor("momentum", domain.FactorTypeTimeSeries, 2, false)}, []string{"BTC"})
		plan.Mode = mode
		_, err := runner.Load(context.Background(), plan)
		require.NoError(t, err)
		got[mode] = seen
	}
	// A live period fails fast as a whole batch.
	require.True(t, got[ModeLive].hasDeadline)
	require.LessOrEqual(t, got[ModeLive].deadline, 10*time.Second)
	require.Zero(t, got[ModeLive].pageTimeout)
	// A backfill batch is bounded per page, never as a whole.
	require.False(t, got[ModeRecalc].hasDeadline)
	require.Equal(t, 10*time.Second, got[ModeRecalc].pageTimeout)
}

func TestLoadLiveReusesPreviousWindow(t *testing.T) {
	set := domain.FactorSet{SetID: "fset", SpaceID: "crypto", SourceDatasetID: "dataset_bars", Freq: "1m", Status: domain.SetStatusEnabled, SubjectMode: domain.SubjectModeAll}
	livePlan := func(target time.Time, universe ...string) Plan {
		plan, err := BuildLivePlan(periodclock.Continuous{}, LiveInput{
			Set: set, Factors: []domain.FactorDef{{FactorID: "f", LookbackPeriods: 3}},
			PeriodTime: target, Universe: universe, CarryColumns: []string{"close"},
		})
		require.NoError(t, err)
		return plan
	}
	var mu sync.Mutex
	starts := map[string]time.Time{}
	store := &fakeStore{read: func(_ context.Context, req storageio.ReadRequest) (map[string]*storageio.Frame, error) {
		frames := emptyFrames(req)
		mu.Lock()
		defer mu.Unlock()
		for _, subject := range req.Subjects {
			starts[subject] = req.Start
			for at := req.Start; at.Before(req.End); at = at.Add(time.Minute) {
				frames[subject].Rows = append(frames[subject].Rows, []any{at, "", float64(at.Minute())})
			}
		}
		return frames, nil
	}}
	runner := NewRunner(store, nil, periodclock.Continuous{}, Config{})
	t0 := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)

	_, err := runner.Load(context.Background(), livePlan(t0, "BTC"))
	require.NoError(t, err)
	require.Equal(t, t0.Add(-2*time.Minute), starts["BTC"])

	t1 := t0.Add(time.Minute)
	loaded, err := runner.Load(context.Background(), livePlan(t1, "BTC", "ETH"))
	require.NoError(t, err)
	require.Equal(t, t1, starts["BTC"], "a warm subject reads only the target bar")
	require.Equal(t, t1.Add(-2*time.Minute), starts["ETH"], "a cold subject reads the whole window")
	for _, subject := range []string{"BTC", "ETH"} {
		rows := loaded.Frames[subject].Rows
		require.Len(t, rows, 3, subject)
		require.Equal(t, t1.Add(-2*time.Minute), rows[0][0])
		require.Equal(t, t1, rows[2][0])
	}

	t3 := t1.Add(2 * time.Minute)
	loaded, err = runner.Load(context.Background(), livePlan(t3, "BTC"))
	require.NoError(t, err)
	require.Equal(t, t1.Add(time.Minute), starts["BTC"], "a skipped period reads from the end of the cached window")
	require.Len(t, loaded.Frames["BTC"].Rows, 3)

	t9 := t3.Add(6 * time.Minute)
	_, err = runner.Load(context.Background(), livePlan(t9, "BTC"))
	require.NoError(t, err)
	require.Equal(t, t9.Add(-2*time.Minute), starts["BTC"], "a window past the cache is read in full")
}

func TestLoadLiveOlderPeriodKeepsNewerWindow(t *testing.T) {
	set := domain.FactorSet{SetID: "fset", SpaceID: "crypto", SourceDatasetID: "dataset_bars", Freq: "1m", Status: domain.SetStatusEnabled, SubjectMode: domain.SubjectModeAll}
	livePlan := func(target time.Time) Plan {
		plan, err := BuildLivePlan(periodclock.Continuous{}, LiveInput{
			Set: set, Factors: []domain.FactorDef{{FactorID: "f", LookbackPeriods: 3}},
			PeriodTime: target, Universe: []string{"BTC"}, CarryColumns: []string{"close"},
		})
		require.NoError(t, err)
		return plan
	}
	var start time.Time
	store := &fakeStore{read: func(_ context.Context, req storageio.ReadRequest) (map[string]*storageio.Frame, error) {
		start = req.Start
		return emptyFrames(req), nil
	}}
	runner := NewRunner(store, nil, periodclock.Continuous{}, Config{})
	t0 := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	for _, target := range []time.Time{t0, t0.Add(-time.Minute)} {
		_, err := runner.Load(context.Background(), livePlan(target))
		require.NoError(t, err)
	}
	_, err := runner.Load(context.Background(), livePlan(t0.Add(time.Minute)))
	require.NoError(t, err)
	require.Equal(t, t0.Add(time.Minute), start)
}
