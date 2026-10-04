package store

import (
	"context"
	"fmt"
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

func TestPeriodStorageStateListWaitingUsesStableIndexedCursor(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	states := []domain.PeriodStorageState{
		testPeriodStorageState(base, domain.PeriodStatusWaiting),
		testPeriodStorageState(base.Add(time.Minute), domain.PeriodStatusWaiting),
		testPeriodStorageState(base.Add(2*time.Minute), domain.PeriodStatusWaiting),
	}
	for _, state := range states {
		require.NoError(t, s.PeriodStorageStates().ObservePeriodStorageState(ctx, state))
	}
	terminal := testPeriodStorageState(base.Add(3*time.Minute), domain.PeriodStatusComplete)
	require.NoError(t, s.PeriodStorageStates().ObservePeriodStorageState(ctx, terminal))

	page, err := s.PeriodStorageStates().ListWaiting(ctx, "crypto", nil, 2)
	require.NoError(t, err)
	require.Len(t, page, 2)
	require.Equal(t, states[0].Key.PeriodTime, page[0].Key.PeriodTime)
	require.Equal(t, states[1].Key.PeriodTime, page[1].Key.PeriodTime)
	cursor := &PeriodStorageStateCursor{DatasetID: page[1].Key.DatasetID, Frequency: page[1].Key.Frequency, PeriodTime: page[1].Key.PeriodTime}
	page, err = s.PeriodStorageStates().ListWaiting(ctx, "crypto", cursor, 2)
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.Equal(t, states[2].Key.PeriodTime, page[0].Key.PeriodTime)

	var plan []struct {
		Detail string `gorm:"column:detail"`
	}
	require.NoError(t, s.db.Raw(`EXPLAIN QUERY PLAN SELECT c_space_id,c_dataset_id,c_frequency,c_period_time,c_series_hash,c_expected_count,c_deadline_at,c_status,c_confirmed_at
		FROM t_collector_period_storage_states
		WHERE c_space_id = ? AND c_status = 'waiting' AND (c_dataset_id,c_frequency,c_period_time) > (?,?,?)
		ORDER BY c_dataset_id,c_frequency,c_period_time LIMIT ?`,
		"crypto", cursor.DatasetID, cursor.Frequency, cursor.PeriodTime, 100).Scan(&plan).Error)
	planText := fmt.Sprint(plan)
	require.Contains(t, planText, "idx_collector_period_storage_waiting_cursor")
	require.NotContains(t, planText, "TEMP B-TREE")
}

func TestPeriodStorageStateWaitingCursorDoesNotMoveWhenConfirmationsChange(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 101; i++ {
		state := testPeriodStorageState(base.Add(time.Duration(i)*time.Minute), domain.PeriodStatusWaiting)
		require.NoError(t, s.PeriodStorageStates().ObservePeriodStorageState(ctx, state))
	}

	page, err := s.PeriodStorageStates().ListWaiting(ctx, "crypto", nil, 100)
	require.NoError(t, err)
	require.Len(t, page, 100)
	cursor := &PeriodStorageStateCursor{
		DatasetID:  page[99].Key.DatasetID,
		Frequency:  page[99].Key.Frequency,
		PeriodTime: page[99].Key.PeriodTime,
	}
	for i, state := range page {
		state.ConfirmedAt = base.Add(24*time.Hour + time.Duration(i)*time.Second)
		require.NoError(t, s.PeriodStorageStates().ObservePeriodStorageState(ctx, state))
	}

	next, err := s.PeriodStorageStates().ListWaiting(ctx, "crypto", cursor, 100)
	require.NoError(t, err)
	require.Len(t, next, 1)
	require.Equal(t, base.Add(100*time.Minute), next[0].Key.PeriodTime)
}

func TestPeriodStorageStateLatestTerminalUsesExactScopeAndSkipsWaiting(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	states := []domain.PeriodStorageState{
		testPeriodStorageState(base, domain.PeriodStatusComplete),
		testPeriodStorageState(base.Add(time.Minute), domain.PeriodStatusWaiting),
		testPeriodStorageState(base.Add(2*time.Minute), domain.PeriodStatusDegraded),
	}
	for _, state := range states {
		require.NoError(t, s.PeriodStorageStates().ObservePeriodStorageState(ctx, state))
	}
	otherFrequency := testPeriodStorageState(base.Add(3*time.Minute), domain.PeriodStatusComplete)
	otherFrequency.Key.Frequency = "5m"
	require.NoError(t, s.PeriodStorageStates().ObservePeriodStorageState(ctx, otherFrequency))
	otherDataset := testPeriodStorageState(base.Add(4*time.Minute), domain.PeriodStatusComplete)
	otherDataset.Key.DatasetID = "another-dataset"
	require.NoError(t, s.PeriodStorageStates().ObservePeriodStorageState(ctx, otherDataset))
	otherSpace := testPeriodStorageState(base.Add(5*time.Minute), domain.PeriodStatusComplete)
	otherSpace.Key.SpaceID = "another-space"
	require.NoError(t, s.PeriodStorageStates().ObservePeriodStorageState(ctx, otherSpace))

	latest, err := s.PeriodStorageStates().LatestTerminalPeriod(ctx, "crypto", "bars", "1m")
	require.NoError(t, err)
	require.NotNil(t, latest)
	require.Equal(t, base.Add(2*time.Minute), latest.Key.PeriodTime)
	require.Equal(t, domain.PeriodStatusDegraded, latest.Status)
}

func TestPeriodStorageStateLatestTerminalReturnsEmptyWithoutTerminal(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	waiting := testPeriodStorageState(time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC), domain.PeriodStatusWaiting)
	require.NoError(t, s.PeriodStorageStates().ObservePeriodStorageState(ctx, waiting))

	latest, err := s.PeriodStorageStates().LatestTerminalPeriod(ctx, "crypto", "bars", "1m")
	require.NoError(t, err)
	require.Nil(t, latest)
}

func testPeriodStorageState(period time.Time, status string) domain.PeriodStorageState {
	return domain.PeriodStorageState{
		Key:        domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period},
		SeriesHash: "abc", ExpectedCount: 2, DeadlineAt: period.Add(time.Minute), Status: status, ConfirmedAt: period.Add(time.Second),
	}
}
