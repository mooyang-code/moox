package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestPeriodStorageStateObservationIsMonotonicAndSurvivesRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	ctx := context.Background()
	period := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	deadline := period.Add(time.Minute)
	waiting := domain.PeriodStorageState{
		Key:        domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period},
		SeriesHash: "abc", ExpectedCount: 2, DeadlineAt: deadline, Status: domain.PeriodStatusWaiting, ConfirmedAt: period.Add(time.Second),
	}
	terminal := waiting
	terminal.Status = domain.PeriodStatusComplete
	terminal.ConfirmedAt = period.Add(2 * time.Minute)

	s := openCollectorStoreAt(t, dbPath)
	require.NoError(t, s.PeriodStorageStates().ObservePeriodStorageState(ctx, waiting))
	require.NoError(t, s.PeriodStorageStates().ObservePeriodStorageState(ctx, terminal))
	require.NoError(t, s.Close())

	reopened := openCollectorStoreAt(t, dbPath)
	require.NoError(t, reopened.PeriodStorageStates().ObservePeriodStorageState(ctx, waiting))
	stored, found, err := reopened.PeriodStorageStates().GetPeriodStorageState(ctx, waiting.Key)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, domain.PeriodStatusComplete, stored.Status, "a stale waiting response cannot roll back persisted terminal authority")
	require.Equal(t, deadline, stored.DeadlineAt)
	require.Equal(t, terminal.ConfirmedAt, stored.ConfirmedAt)
}

func TestPeriodStorageStateRejectsHashCountAndDeadlineConflicts(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	state := testPeriodStorageState(time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC), domain.PeriodStatusWaiting)
	require.NoError(t, s.PeriodStorageStates().ObservePeriodStorageState(ctx, state))
	for _, mutate := range []func(*domain.PeriodStorageState){
		func(got *domain.PeriodStorageState) { got.SeriesHash = "different" },
		func(got *domain.PeriodStorageState) { got.ExpectedCount++ },
		func(got *domain.PeriodStorageState) { got.DeadlineAt = got.DeadlineAt.Add(time.Second) },
	} {
		conflicting := state
		mutate(&conflicting)
		require.ErrorIs(t, s.PeriodStorageStates().ObservePeriodStorageState(ctx, conflicting), ErrPeriodStorageStateConflict)
	}
}

func TestPeriodStorageStateConcurrentWaitingAndTerminalObservationKeepsTerminal(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	first := openCollectorStoreAt(t, dbPath)
	second := openCollectorStoreAt(t, dbPath)
	waiting := testPeriodStorageState(time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC), domain.PeriodStatusWaiting)
	terminal := waiting
	terminal.Status = domain.PeriodStatusDegraded
	terminal.ConfirmedAt = waiting.DeadlineAt.Add(time.Second)
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, entry := range []struct {
		repo  *Store
		state domain.PeriodStorageState
	}{{first, waiting}, {second, terminal}} {
		wg.Add(1)
		go func(repo *Store, state domain.PeriodStorageState) {
			defer wg.Done()
			<-start
			errs <- repo.PeriodStorageStates().ObservePeriodStorageState(context.Background(), state)
		}(entry.repo, entry.state)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	stored, found, err := first.PeriodStorageStates().GetPeriodStorageState(context.Background(), waiting.Key)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, domain.PeriodStatusDegraded, stored.Status)
}

func testPeriodStorageState(period time.Time, status string) domain.PeriodStorageState {
	return domain.PeriodStorageState{
		Key:        domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period},
		SeriesHash: "abc", ExpectedCount: 2, DeadlineAt: period.Add(time.Minute), Status: status, ConfirmedAt: period.Add(time.Second),
	}
}
