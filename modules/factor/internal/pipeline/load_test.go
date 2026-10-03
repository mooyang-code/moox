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
			{FactorID: "short", LookbackPeriods: 5, Status: domain.FactorStatusEnabled},
			{FactorID: "long", LookbackPeriods: 20, Status: domain.FactorStatusEnabled},
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
