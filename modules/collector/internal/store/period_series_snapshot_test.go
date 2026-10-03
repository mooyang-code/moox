package store

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/stretchr/testify/require"
)

func TestPeriodSeriesSnapshotIsImmutableAndNextPeriodUsesNewMembership(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	period := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

	first, created, err := s.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, testPeriodSnapshot(period, "BTC-USDT", "ETH-USDT"))
	require.NoError(t, err)
	require.True(t, created)
	require.Len(t, first.Entries, 2)

	changedMembership := testPeriodSnapshot(period, "BTC-USDT")
	_, created, err = s.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, changedMembership)
	require.ErrorIs(t, err, ErrPeriodSeriesSnapshotConflict)
	require.False(t, created)

	stored, found, err := s.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, first.Key)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, first, stored, "membership refresh must not change the existing period snapshot")
	require.Equal(t, first.SeriesHash, stored.SeriesHash)
	require.Equal(t, uint32(0), stored.Entries[0].SeriesIndex)
	require.Equal(t, uint32(1), stored.Entries[1].SeriesIndex)
	require.Equal(t, uint32(2), stored.ExpectedCount)

	nextPeriod := period.Add(time.Minute)
	next, nextCreated, err := s.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, testPeriodSnapshot(nextPeriod, "BTC-USDT"))
	require.NoError(t, err)
	require.True(t, nextCreated)
	require.Len(t, next.Entries, 1)
	require.Equal(t, "BTC-USDT", next.Entries[0].SubjectID)
	require.Equal(t, uint32(1), next.ExpectedCount)
}

func TestPeriodSeriesSnapshotSeparatesMonthAndMinuteFrequency(t *testing.T) {
	ctx := context.Background()
	s := newCollectorStore(t)
	period := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	month := testPeriodSnapshot(period, "BTC-USDT")
	month.Key.Frequency = "1M"
	month.Entries[0].Frequency = "1M"
	minute := testPeriodSnapshot(period, "BTC-USDT")

	storedMonth, created, err := s.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, month)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, "1M", storedMonth.Key.Frequency)
	storedMinute, created, err := s.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, minute)
	require.NoError(t, err)
	require.True(t, created, "a minute snapshot at the same timestamp must not collide with a month snapshot")
	require.Equal(t, "1m", storedMinute.Key.Frequency)

	loadedMonth, found, err := s.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1M", PeriodTime: period})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "1M", loadedMonth.Entries[0].Frequency)
	loadedMinute, found, err := s.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "1m", loadedMinute.Entries[0].Frequency)
}

func TestPeriodSeriesSnapshotPreservesHourlyFrequencyIdentity(t *testing.T) {
	ctx := context.Background()
	s := newCollectorStore(t)
	period := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	historical := testPeriodSnapshot(period, "BTC-USDT")
	historical.Key.Frequency = "1H"
	historical.Entries[0].Frequency = "1H"
	canonical := testPeriodSnapshot(period, "BTC-USDT")
	canonical.Key.Frequency = "1h"
	canonical.Entries[0].Frequency = "1h"

	storedHistorical, created, err := s.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, historical)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, "1H", storedHistorical.Key.Frequency)
	storedCanonical, created, err := s.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, canonical)
	require.NoError(t, err)
	require.True(t, created, "the canonical 1h key must not alias the catalog's historical 1H spelling")
	require.Equal(t, "1h", storedCanonical.Key.Frequency)

	loaded, found, err := s.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, historical.Key)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "1H", loaded.Entries[0].Frequency)
}

func TestCreatePeriodSeriesSnapshotIfAbsentIsIdempotentForConcurrentSameSnapshot(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	firstStore := openCollectorStoreAt(t, dbPath)
	secondStore := openCollectorStoreAt(t, dbPath)
	period := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	input := testPeriodSnapshot(period, "BTC-USDT", "ETH-USDT", "SOL-USDT")

	start := make(chan struct{})
	results := make(chan domain.PeriodSeriesSnapshot, 2)
	errs := make(chan error, 2)
	created := make(chan bool, 2)
	var wg sync.WaitGroup
	for _, repo := range []*Store{firstStore, secondStore} {
		wg.Add(1)
		go func(repo *Store) {
			defer wg.Done()
			<-start
			rows, wasCreated, err := repo.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(context.Background(), input)
			results <- rows
			created <- wasCreated
			errs <- err
		}(repo)
	}
	close(start)
	wg.Wait()
	close(results)
	close(created)
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}
	rows := make([]domain.PeriodSeriesSnapshot, 0, 2)
	for result := range results {
		rows = append(rows, result)
	}
	require.Len(t, rows, 2)
	require.Equal(t, rows[0], rows[1], "concurrent callers must receive the same persisted rows")
	createdCount := 0
	for wasCreated := range created {
		if wasCreated {
			createdCount++
		}
	}
	require.Equal(t, 1, createdCount)

	stored, found, err := firstStore.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(context.Background(), input.Key)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, rows[0], stored)
}

func TestPeriodSeriesSnapshotSurvivesStoreReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	firstStore := openCollectorStoreAt(t, dbPath)
	input := testPeriodSnapshot(time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC), "BTC-USDT", "ETH-USDT")
	saved, created, err := firstStore.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(context.Background(), input)
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, firstStore.Close())

	reopened := openCollectorStoreAt(t, dbPath)
	loaded, found, err := reopened.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(context.Background(), input.Key)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, saved, loaded, "the snapshot envelope and persisted entry metadata survive a fresh database connection")
}

func TestCreatePeriodSeriesSnapshotIfAbsentRejectsDifferentSnapshot(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	period := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	first := testPeriodSnapshot(period, "BTC-USDT", "ETH-USDT")
	persistedFirst, created, err := s.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, first)
	require.NoError(t, err)
	require.True(t, created)

	_, created, err = s.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, testPeriodSnapshot(period, "BTC-USDT", "SOL-USDT"))
	require.ErrorIs(t, err, ErrPeriodSeriesSnapshotConflict)
	require.False(t, created)
	stored, found, err := s.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, first.Key)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, persistedFirst, stored)
}

func TestCreatePeriodSeriesSnapshotIfAbsentValidatesDenseIndexHashAndCount(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func([]domain.PeriodSeriesSnapshotEntry) []domain.PeriodSeriesSnapshotEntry
	}{
		{name: "dense index", mutate: func(rows []domain.PeriodSeriesSnapshotEntry) []domain.PeriodSeriesSnapshotEntry {
			rows[1].SeriesIndex = 2
			return rows
		}},
		{name: "series hash", mutate: func(rows []domain.PeriodSeriesSnapshotEntry) []domain.PeriodSeriesSnapshotEntry {
			rows[0].SeriesHash = "wrong"
			rows[1].SeriesHash = "wrong"
			return rows
		}},
		{name: "expected count", mutate: func(rows []domain.PeriodSeriesSnapshotEntry) []domain.PeriodSeriesSnapshotEntry {
			rows[0].ExpectedCount++
			rows[1].ExpectedCount++
			return rows
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := newCollectorStore(t)
			snapshot := testPeriodSnapshot(time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC), "BTC-USDT", "ETH-USDT")
			snapshot.Entries = test.mutate(snapshot.Entries)
			_, _, err := s.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(context.Background(), snapshot)
			require.Error(t, err)
		})
	}
}

func TestPeriodSeriesSnapshotContractValidatesEnvelopeAndDistinguishesAbsent(t *testing.T) {
	ctx := context.Background()
	period := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	s := newCollectorStore(t)
	input := testPeriodSnapshot(period, "BTC-USDT", "ETH-USDT")
	snapshot, found, err := s.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, input.Key)
	require.NoError(t, err)
	require.False(t, found)
	require.Zero(t, snapshot)

	for _, test := range []struct {
		name   string
		mutate func(*domain.PeriodSeriesSnapshot)
	}{
		{name: "key", mutate: func(snapshot *domain.PeriodSeriesSnapshot) { snapshot.Key.DatasetID = "other" }},
		{name: "hash", mutate: func(snapshot *domain.PeriodSeriesSnapshot) { snapshot.SeriesHash = "wrong" }},
		{name: "count", mutate: func(snapshot *domain.PeriodSeriesSnapshot) { snapshot.ExpectedCount++ }},
		{name: "empty", mutate: func(snapshot *domain.PeriodSeriesSnapshot) { snapshot.Entries = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid := input
			test.mutate(&invalid)
			_, created, err := s.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, invalid)
			require.Error(t, err)
			require.False(t, created)
		})
	}

	input.Key.SpaceID = " crypto "
	input.Key.DatasetID = " bars "
	input.Key.Frequency = " 1M "
	for index := range input.Entries {
		input.Entries[index].Frequency = " 1M "
	}
	input.Key.PeriodTime = period.In(time.FixedZone("UTC+8", 8*60*60))
	input.SeriesHash = strings.ToUpper(input.SeriesHash)
	snapshot, created, err := s.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, input)
	require.NoError(t, err)
	require.True(t, created)
	wantKey := testPeriodSnapshot(period, "BTC-USDT", "ETH-USDT").Key
	wantKey.Frequency = "1M"
	require.Equal(t, wantKey, snapshot.Key)
	require.Equal(t, uint32(len(snapshot.Entries)), snapshot.ExpectedCount)
	require.Equal(t, snapshot.Entries[0].SeriesHash, snapshot.SeriesHash)
	for _, entry := range snapshot.Entries {
		require.Positive(t, entry.ID)
		require.False(t, entry.CreateTime.IsZero())
	}

	require.NoError(t, s.db.Model(&domain.PeriodSeriesSnapshotEntry{}).Where("c_dataset_id = ?", "bars").Update("c_series_hash", "corrupted").Error)
	_, found, err = s.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, snapshot.Key)
	require.True(t, found)
	require.ErrorContains(t, err, "stored period series snapshot is invalid", "a corrupt existing period must not look absent")
	require.NoError(t, s.db.Model(&domain.PeriodSeriesSnapshotEntry{}).Where("c_dataset_id = ?", "bars").Update("c_series_hash", snapshot.SeriesHash).Error)
	require.NoError(t, s.db.Where("c_dataset_id = ? AND c_series_index = ?", "bars", 1).Delete(&domain.PeriodSeriesSnapshotEntry{}).Error)
	_, found, err = s.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, snapshot.Key)
	require.True(t, found)
	require.ErrorContains(t, err, "stored period series snapshot is invalid", "a partial persisted snapshot must not look absent")
}

func TestPeriodSeriesSnapshotCleanupRequiresStorageTerminalState(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	oldWithoutReadiness := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	oldWithReadiness := oldWithoutReadiness.Add(time.Minute)
	_, _, err := s.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, testPeriodSnapshot(oldWithoutReadiness, "BTC-USDT"))
	require.NoError(t, err)
	_, _, err = s.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, testPeriodSnapshot(oldWithReadiness, "ETH-USDT"))
	require.NoError(t, err)
	require.NoError(t, s.db.Model(&domain.PeriodSeriesSnapshotEntry{}).
		Where("c_space_id = ? AND c_dataset_id = ? AND c_frequency = ?", "crypto", "bars", "1m").
		Update("c_ctime", oldWithoutReadiness).Error)
	// Readiness and age are local signals, not evidence that Storage finalized the period.
	require.NoError(t, s.db.Exec(`INSERT INTO t_period_readiness (c_space_id, c_dataset_id, c_frequency, c_work_type, c_period_time, c_deadline_at, c_status, c_report_state, c_collected_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, "crypto", "bars", "1m", "collection", oldWithReadiness, oldWithReadiness.Add(time.Minute), domain.PeriodStatusComplete, domain.PeriodReportReported, oldWithReadiness.Add(time.Hour)).Error)

	deleted, err := s.PeriodSeriesSnapshot().CleanupTerminalBefore(ctx, "crypto", oldWithReadiness.Add(24*time.Hour), 1000)
	require.NoError(t, err)
	require.Zero(t, deleted)
	for _, period := range []time.Time{oldWithoutReadiness, oldWithReadiness} {
		snapshot, found, err := s.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period})
		require.NoError(t, err)
		require.True(t, found, "period %s must survive without matching terminal Storage state", period)
		require.Len(t, snapshot.Entries, 1)
	}
}

func TestPeriodSeriesSnapshotCleanupRetentionUsesSnapshotAndConfirmationAge(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-30 * 24 * time.Hour)
	fixtures := []struct {
		frequency string
		period    time.Time
	}{
		{frequency: "1D", period: now.Add(-2 * 24 * time.Hour)},
		{frequency: "1W", period: now.Add(-8 * 24 * time.Hour)},
		{frequency: "1M", period: now.Add(-32 * 24 * time.Hour)},
	}
	for _, fixture := range fixtures {
		snapshot := testPeriodSnapshot(fixture.period, "BTC-USDT")
		snapshot.Key.Frequency = fixture.frequency
		snapshot.Entries[0].Frequency = fixture.frequency
		stored, _, err := s.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, snapshot)
		require.NoError(t, err)
		require.NoError(t, s.PeriodStorageStates().ObservePeriodStorageState(ctx, domain.PeriodStorageState{
			Key: stored.Key, SeriesHash: stored.SeriesHash, ExpectedCount: stored.ExpectedCount,
			DeadlineAt: fixture.period.Add(time.Minute), Status: domain.PeriodStatusComplete, ConfirmedAt: now,
		}))
	}
	deleted, err := s.PeriodSeriesSnapshot().CleanupTerminalBefore(ctx, "crypto", cutoff, 10)
	require.NoError(t, err)
	require.Zero(t, deleted, "old 1D/1W/1M period timestamps do not make fresh snapshots immediately collectible")

	retainedAt := cutoff.Add(-time.Second)
	for _, fixture := range fixtures {
		require.NoError(t, s.db.Model(&domain.PeriodSeriesSnapshotEntry{}).
			Where("c_space_id = ? AND c_dataset_id = ? AND c_frequency = ? AND c_period_time = ?", "crypto", "bars", fixture.frequency, fixture.period).
			Update("c_ctime", retainedAt).Error)
		require.NoError(t, s.db.Table("t_collector_period_storage_states").
			Where("c_space_id = ? AND c_dataset_id = ? AND c_frequency = ? AND c_period_time = ?", "crypto", "bars", fixture.frequency, fixture.period).
			Update("c_confirmed_at", retainedAt).Error)
	}
	deleted, err = s.PeriodSeriesSnapshot().CleanupTerminalBefore(ctx, "crypto", cutoff, 10)
	require.NoError(t, err)
	require.EqualValues(t, 6, deleted, "cleanup reports the three snapshot rows and their three Storage-state rows")
	var snapshotRows, stateRows int64
	require.NoError(t, s.db.Table("t_collector_task_period_series").Count(&snapshotRows).Error)
	require.NoError(t, s.db.Table("t_collector_period_storage_states").Count(&stateRows).Error)
	require.Zero(t, snapshotRows)
	require.Zero(t, stateRows)
}

func TestPeriodCleanupRetainsTerminalEvidenceWhileKlineWriteTargetExists(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	period := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	snapshot, _, err := s.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, testPeriodSnapshot(period, "BTC-USDT"))
	require.NoError(t, err)
	require.NoError(t, s.db.Model(&domain.PeriodSeriesSnapshotEntry{}).
		Where("c_space_id = ? AND c_dataset_id = ? AND c_frequency = ? AND c_period_time = ?", "crypto", "bars", "1m", period).
		Update("c_ctime", period).Error)
	require.NoError(t, s.PeriodStorageStates().ObservePeriodStorageState(ctx, domain.PeriodStorageState{
		Key: snapshot.Key, SeriesHash: snapshot.SeriesHash, ExpectedCount: snapshot.ExpectedCount,
		DeadlineAt: period.Add(time.Minute), Status: domain.PeriodStatusComplete, ConfirmedAt: period,
	}))
	require.NoError(t, s.Tasks().Create(ctx, domain.CollectionTask{
		SpaceID: "crypto", TaskID: "task-retained", TaskName: "Retained", DataType: "kline", CollectParams: `{}`, Enabled: true,
	}))
	require.NoError(t, s.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{{
		SpaceID: "crypto", InstanceID: "instance-retained", DataType: "kline", SubjectID: "BTC-USDT", Frequency: "1m",
		TargetDataTime: &period, TaskParams: `{}`,
	}}))
	require.NoError(t, s.TaskInstances().UpsertWriteTargets(ctx, []domain.WriteTarget{{
		ID: "target-retained", SpaceID: "crypto", InstanceID: "instance-retained", TaskID: "task-retained", DatasetID: "bars", Status: "succeeded",
	}}))

	cutoff := period.Add(31 * 24 * time.Hour)
	deleted, err := s.PeriodSeriesSnapshot().CleanupTerminalBefore(ctx, "crypto", cutoff, 100)
	require.NoError(t, err)
	require.Zero(t, deleted, "terminal period proof is still needed by the surviving Kline WriteTarget")
	_, found, err := s.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, snapshot.Key)
	require.NoError(t, err)
	require.True(t, found)
	_, found, err = s.PeriodStorageStates().GetPeriodStorageState(ctx, snapshot.Key)
	require.NoError(t, err)
	require.True(t, found)

	require.NoError(t, s.db.Exec(`DELETE FROM t_collector_instance_write_targets WHERE c_space_id = ? AND c_write_target_id = ?`, "crypto", "target-retained").Error)
	deleted, err = s.PeriodSeriesSnapshot().CleanupTerminalBefore(ctx, "crypto", cutoff, 100)
	require.NoError(t, err)
	require.EqualValues(t, 2, deleted, "period evidence becomes collectible after its WriteTarget is gone")
}

func TestListCleanupCandidatesSkipsRecentlyConfirmedTerminalStoragePeriods(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	period := now.Add(-32 * 24 * time.Hour)
	snapshot := testPeriodSnapshot(period, "BTC-USDT")
	stored, _, err := s.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, snapshot)
	require.NoError(t, err)
	require.NoError(t, s.PeriodStorageStates().ObservePeriodStorageState(ctx, domain.PeriodStorageState{
		Key: stored.Key, SeriesHash: stored.SeriesHash, ExpectedCount: stored.ExpectedCount,
		DeadlineAt: period.Add(time.Hour), Status: domain.PeriodStatusComplete, ConfirmedAt: now,
	}))

	page, err := s.PeriodSeriesSnapshot().ListCleanupCandidates(ctx, "crypto", now.Add(-30*24*time.Hour), nil, 10)
	require.NoError(t, err)
	require.Empty(t, page.Snapshots, "a recently confirmed monthly period should not be probed again before retention expires")
}

func TestCleanupTerminalBeforeRequiresMatchingStorageStateAndProtectsPendingWork(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	periods := map[string]time.Time{
		"complete":          base,
		"degraded":          base.Add(time.Minute),
		"waiting":           base.Add(2 * time.Minute),
		"hash-mismatch":     base.Add(3 * time.Minute),
		"count-mismatch":    base.Add(4 * time.Minute),
		"pending-retry":     base.Add(5 * time.Minute),
		"failed-unreported": base.Add(6 * time.Minute),
		"planned-batch":     base.Add(7 * time.Minute),
		"dispatched-batch":  base.Add(8 * time.Minute),
		"planned-manifest":  base.Add(9 * time.Minute),
		"other-space":       base.Add(9 * time.Minute),
		"recent":            base.Add(48 * time.Hour),
	}
	snapshots := make(map[string]domain.PeriodSeriesSnapshot, len(periods))
	for name, period := range periods {
		spaceID := "crypto"
		if name == "other-space" {
			spaceID = "research"
		}
		snapshot := testPeriodSnapshot(period, "BTC-USDT")
		snapshot.Key.SpaceID = spaceID
		snapshot.Entries[0].SpaceID = spaceID
		stored, _, err := s.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, snapshot)
		require.NoError(t, err)
		require.NoError(t, s.db.Model(&domain.PeriodSeriesSnapshotEntry{}).
			Where("c_space_id = ? AND c_dataset_id = ? AND c_frequency = ? AND c_period_time = ?", spaceID, "bars", "1m", period).
			Update("c_ctime", period).Error)
		snapshots[name] = stored
	}
	for _, name := range []string{"complete", "degraded", "waiting", "hash-mismatch", "count-mismatch", "other-space", "recent", "pending-retry", "failed-unreported", "planned-batch", "dispatched-batch", "planned-manifest"} {
		status := domain.PeriodStatusComplete
		if name == "degraded" {
			status = domain.PeriodStatusDegraded
		}
		if name == "waiting" {
			status = domain.PeriodStatusWaiting
		}
		state := storageStateForSnapshot(snapshots[name], status)
		switch name {
		case "hash-mismatch":
			state.SeriesHash = "different"
		case "count-mismatch":
			state.ExpectedCount++
		}
		require.NoError(t, s.PeriodStorageStates().ObservePeriodStorageState(ctx, state))
	}
	require.NoError(t, s.FetchRetries().Upsert(ctx, &domain.RetryItem{
		SpaceID: "crypto", RetryKey: "pending-period", SubjectID: "BTC-USDT", Frequency: "1m",
		TargetDataTime: periods["pending-retry"], Status: "pending", FailureTargetsJSON: "[]", CreateTime: base,
	}))
	require.NoError(t, s.FetchRetries().Upsert(ctx, &domain.RetryItem{
		SpaceID: "crypto", RetryKey: "failed-period", SubjectID: "BTC-USDT", Frequency: "1m",
		TargetDataTime: periods["failed-unreported"], Status: "permanent_failed", FailureTargetsJSON: "[]", CreateTime: base,
	}))
	for index, name := range []string{"planned-batch", "dispatched-batch"} {
		instanceID := "instance-" + name
		batchID := "batch-" + name
		targetTime := periods[name]
		require.NoError(t, s.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{{
			SpaceID: "crypto", InstanceID: instanceID, SubjectID: "BTC-USDT", Frequency: "1m",
			TargetDataTime: &targetTime, TaskParams: `{}`,
		}}))
		created, err := s.FetchBatches().CreatePlanned(ctx, &domain.BatchInvocation{SpaceID: "crypto", BatchID: batchID, ScheduleID: batchID, BatchKind: domain.BatchKindRealtime, Frequency: "1m", Status: domain.BatchStatusPlanned})
		require.NoError(t, err)
		require.True(t, created)
		require.NoError(t, s.FetchBatches().UpsertItems(ctx, "crypto", batchID, []string{instanceID}))
		if index == 1 {
			updated, err := s.FetchBatches().MarkDispatchedToNode(ctx, "crypto", batchID, "req", base.Add(time.Minute), "region", "node", "fn")
			require.NoError(t, err)
			require.True(t, updated)
		}
	}
	for _, manifest := range []struct {
		name   string
		status domain.BatchStatus
	}{
		{name: "complete", status: domain.BatchStatusSucceeded},
		{name: "planned-manifest", status: domain.BatchStatusPlanned},
	} {
		period := periods[manifest.name]
		batchID := "timer-manifest-" + manifest.name
		require.NoError(t, s.db.Create(&domain.BatchInvocation{
			SpaceID: "crypto", BatchID: batchID, ScheduleID: batchID, BatchKind: domain.BatchKindRealtime,
			Frequency: "1m", Status: manifest.status,
		}).Error)
		require.NoError(t, s.db.Create(&domain.TimerPeriodBatch{
			Key: "timer-key-" + manifest.name, SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period,
			TaskID: "task-" + manifest.name, FirstRunID: "run-" + manifest.name, SeriesHash: snapshots[manifest.name].SeriesHash,
			ExpectedCount: 1, GroupCount: 1, BindingHash: "binding", RouteVersion: "route-v1", BatchID: batchID,
			FunctionName: "timer-function", NodeID: "timer-node", Region: "region", DeadlineAt: period.Add(time.Hour),
		}).Error)
	}
	manifestsDeleted, err := s.TimerPeriodBatches().CleanupStorageTerminal(ctx, "crypto", 1)
	require.NoError(t, err)
	require.EqualValues(t, 1, manifestsDeleted, "terminal manifests use their own bounded cleanup budget")

	deleted, err := s.PeriodSeriesSnapshot().CleanupTerminalBefore(ctx, "crypto", base.Add(24*time.Hour), 1)
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted, "the physical row budget can remove a series entry while retaining state as progress")
	deleted, err = s.PeriodSeriesSnapshot().CleanupTerminalBefore(ctx, "crypto", base.Add(24*time.Hour), 1000)
	require.NoError(t, err)
	require.EqualValues(t, 2, deleted, "the next keyset page removes the remaining degraded period rows")
	deleted, err = s.PeriodSeriesSnapshot().CleanupTerminalBefore(ctx, "crypto", base.Add(24*time.Hour), 1000)
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted, "the cursor wraps to remove the prior period's retained Storage state")
	for _, name := range []string{"waiting", "hash-mismatch", "count-mismatch", "pending-retry", "failed-unreported", "planned-batch", "dispatched-batch", "planned-manifest", "other-space", "recent"} {
		_, found, err := s.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, snapshots[name].Key)
		require.NoError(t, err)
		require.True(t, found, "period %s must remain", name)
	}
	_, found, err := s.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, snapshots["complete"].Key)
	require.NoError(t, err)
	require.False(t, found)
	_, found, err = s.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, snapshots["degraded"].Key)
	require.NoError(t, err)
	require.False(t, found)
	_, found, err = s.PeriodStorageStates().GetPeriodStorageState(ctx, snapshots["complete"].Key)
	require.NoError(t, err)
	require.False(t, found, "terminal state is removed atomically with its period snapshot")
	var completedManifests, plannedManifests int64
	require.NoError(t, s.db.Model(&domain.TimerPeriodBatch{}).Where("c_key = ?", "timer-key-complete").Count(&completedManifests).Error)
	require.Zero(t, completedManifests, "terminal manifest is removed with its confirmed period")
	require.NoError(t, s.db.Model(&domain.TimerPeriodBatch{}).Where("c_key = ?", "timer-key-planned-manifest").Count(&plannedManifests).Error)
	require.EqualValues(t, 1, plannedManifests, "unclaimed planned manifest keeps its period active")
}

func TestPeriodCleanupCountsBoundLargePeriodAndAdvanceFairly(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	cutoff := time.Now().UTC().Add(-30 * 24 * time.Hour)
	period := cutoff.Add(-24 * time.Hour)
	subjects := make([]string, 1201)
	for i := range subjects {
		subjects[i] = fmt.Sprintf("S%04d-USDT", i)
	}
	large, _, err := s.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, testPeriodSnapshot(period, subjects...))
	require.NoError(t, err)
	small, _, err := s.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, testPeriodSnapshot(period.Add(time.Minute), "OTHER-USDT"))
	require.NoError(t, err)
	require.NoError(t, s.db.Model(&domain.PeriodSeriesSnapshotEntry{}).Where("c_space_id = ?", "crypto").Update("c_ctime", period).Error)
	for _, snapshot := range []domain.PeriodSeriesSnapshot{large, small} {
		state := storageStateForSnapshot(snapshot, domain.PeriodStatusComplete)
		state.ConfirmedAt = period
		require.NoError(t, s.PeriodStorageStates().ObservePeriodStorageState(ctx, state))
	}
	counts, err := s.PeriodSeriesSnapshot().CleanupTerminalBeforeWithCounts(ctx, "crypto", cutoff, 500)
	require.NoError(t, err)
	require.Equal(t, PeriodCleanupCounts{SnapshotRows: 500}, counts)
	counts, err = s.PeriodSeriesSnapshot().CleanupTerminalBeforeWithCounts(ctx, "crypto", cutoff, 500)
	require.NoError(t, err)
	require.Equal(t, PeriodCleanupCounts{SnapshotRows: 1, StateRows: 1}, counts)
	_, found, err := s.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, small.Key)
	require.NoError(t, err)
	require.False(t, found, "next pass advances beyond the oversized period before wrapping")
	counts, err = s.PeriodSeriesSnapshot().CleanupTerminalBeforeWithCounts(ctx, "crypto", cutoff, 500)
	require.NoError(t, err)
	require.Equal(t, PeriodCleanupCounts{SnapshotRows: 500}, counts)
	counts, err = s.PeriodSeriesSnapshot().CleanupTerminalBeforeWithCounts(ctx, "crypto", cutoff, 500)
	require.NoError(t, err)
	require.Equal(t, PeriodCleanupCounts{SnapshotRows: 201, StateRows: 1}, counts)
}

func TestCleanupTerminalBeforeHonorsPhysicalRowBudgetAcrossMultiSeriesPeriods(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-30 * 24 * time.Hour)
	periods := []struct {
		period   time.Time
		subjects []string
		status   string
	}{
		{period: now.Add(-40 * 24 * time.Hour), subjects: []string{"BTC-USDT", "ETH-USDT", "SOL-USDT"}, status: domain.PeriodStatusComplete},
		{period: now.Add(-39 * 24 * time.Hour), subjects: []string{"ADA-USDT", "XRP-USDT"}, status: domain.PeriodStatusDegraded},
	}
	stored := make([]domain.PeriodSeriesSnapshot, 0, len(periods))
	for _, fixture := range periods {
		snapshot, _, err := s.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, testPeriodSnapshot(fixture.period, fixture.subjects...))
		require.NoError(t, err)
		stored = append(stored, snapshot)
		require.NoError(t, s.db.Model(&domain.PeriodSeriesSnapshotEntry{}).
			Where("c_space_id = ? AND c_dataset_id = ? AND c_frequency = ? AND c_period_time = ?", "crypto", "bars", "1m", fixture.period).
			Update("c_ctime", cutoff.Add(-time.Hour)).Error)
		require.NoError(t, s.PeriodStorageStates().ObservePeriodStorageState(ctx, domain.PeriodStorageState{
			Key: snapshot.Key, SeriesHash: snapshot.SeriesHash, ExpectedCount: snapshot.ExpectedCount,
			DeadlineAt: fixture.period.Add(time.Minute), Status: fixture.status, ConfirmedAt: cutoff.Add(-time.Hour),
		}))
	}

	require.NoError(t, s.db.Exec(`CREATE TRIGGER fail_state_delete BEFORE DELETE ON t_collector_period_storage_states BEGIN SELECT RAISE(ABORT, 'test rollback'); END`).Error)
	counts, err := s.PeriodSeriesSnapshot().CleanupTerminalBeforeWithCounts(ctx, "crypto", cutoff, 5)
	require.Error(t, err)
	require.Equal(t, PeriodCleanupCounts{}, counts, "failed state deletion rolls back earlier snapshot-entry deletes")
	var unchanged int64
	require.NoError(t, s.db.Model(&domain.PeriodSeriesSnapshotEntry{}).Count(&unchanged).Error)
	require.EqualValues(t, 5, unchanged)
	require.NoError(t, s.db.Exec(`DROP TRIGGER fail_state_delete`).Error)
	counts, err = s.PeriodSeriesSnapshot().CleanupTerminalBeforeWithCounts(ctx, "crypto", cutoff, 5)
	deleted := counts.Total()
	require.NoError(t, err)
	require.Equal(t, PeriodCleanupCounts{SnapshotRows: 4, StateRows: 1}, counts)
	require.EqualValues(t, 5, deleted, "the next candidate can use only the remaining physical-row budget")
	_, found, err := s.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, stored[0].Key)
	require.NoError(t, err)
	require.False(t, found)
	state, found, err := s.PeriodStorageStates().GetPeriodStorageState(ctx, stored[0].Key)
	require.NoError(t, err)
	require.False(t, found, "Storage state is removed only after all snapshot entries fit in budget")
	var remainingSeriesRows int64
	require.NoError(t, s.db.Model(&domain.PeriodSeriesSnapshotEntry{}).
		Where("c_space_id = ? AND c_dataset_id = ? AND c_frequency = ? AND c_period_time = ?", stored[1].Key.SpaceID, stored[1].Key.DatasetID, stored[1].Key.Frequency, stored[1].Key.PeriodTime).
		Count(&remainingSeriesRows).Error)
	require.EqualValues(t, 1, remainingSeriesRows, "oversized progress leaves a prefix and terminal state for the next pass")
	state, found, err = s.PeriodStorageStates().GetPeriodStorageState(ctx, stored[1].Key)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, domain.PeriodStatusDegraded, state.Status)
	page, err := s.PeriodSeriesSnapshot().ListCleanupCandidates(ctx, "crypto", cutoff, nil, 10)
	require.NoError(t, err)
	require.Len(t, page.Snapshots, 1)
	require.Equal(t, stored[1].SeriesHash, page.Snapshots[0].SeriesHash, "the old terminal Storage state supplies identity while rows are partial")
	require.Equal(t, stored[1].ExpectedCount, page.Snapshots[0].ExpectedCount)
	require.Len(t, page.Snapshots[0].Entries, 1)

	deleted, err = s.PeriodSeriesSnapshot().CleanupTerminalBefore(ctx, "crypto", cutoff, 3)
	require.NoError(t, err)
	require.EqualValues(t, 2, deleted, "the final series row and state fit in the next physical budget")
	_, found, err = s.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, stored[1].Key)
	require.NoError(t, err)
	require.False(t, found)
}

func TestTimerManifestSurvivesBatchRetentionUntilPeriodCleanup(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	period := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	snapshot, _, err := s.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, testPeriodSnapshot(period, "BTC-USDT"))
	require.NoError(t, err)
	require.NoError(t, s.PeriodStorageStates().ObservePeriodStorageState(ctx, storageStateForSnapshot(snapshot, domain.PeriodStatusComplete)))
	completedAt := period.Add(time.Hour)
	batchID := "batch-retained-by-manifest"
	require.NoError(t, s.db.Create(&domain.BatchInvocation{
		SpaceID: "crypto", BatchID: batchID, ScheduleID: batchID, BatchKind: domain.BatchKindRealtime,
		Frequency: "1m", Status: domain.BatchStatusSucceeded, CompletedAt: &completedAt,
	}).Error)
	require.NoError(t, s.db.Create(&domain.TimerPeriodBatch{
		Key: "manifest-retained-by-batch", SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period,
		TaskID: "task-retained", FirstRunID: "run-retained", SeriesHash: snapshot.SeriesHash, ExpectedCount: 1,
		GroupCount: 1, BindingHash: "binding", RouteVersion: "route-v1", BatchID: batchID,
		FunctionName: "timer-function", NodeID: "timer-node", Region: "region", DeadlineAt: period.Add(time.Hour),
	}).Error)

	retentionCutoff := period.Add(24 * time.Hour)
	require.NoError(t, s.FetchBatches().Cleanup(ctx, retentionCutoff))
	_, err = s.FetchBatches().Get(ctx, "crypto", batchID)
	require.NoError(t, err, "batch retention must not orphan a live period manifest")
	require.NoError(t, s.db.Model(&domain.PeriodSeriesSnapshotEntry{}).
		Where("c_space_id = ? AND c_dataset_id = ? AND c_frequency = ? AND c_period_time = ?", "crypto", "bars", "1m", period).
		Update("c_ctime", period).Error)

	cutoff := period.Add(31 * 24 * time.Hour)
	deleted, err := s.PeriodSeriesSnapshot().CleanupTerminalBefore(ctx, "crypto", cutoff, 10)
	require.NoError(t, err)
	require.Zero(t, deleted, "snapshot cleanup must not delete Timer manifests outside their separate budget")
	var manifestCount int64
	require.NoError(t, s.db.Model(&domain.TimerPeriodBatch{}).Where("c_key = ?", "manifest-retained-by-batch").Count(&manifestCount).Error)
	require.EqualValues(t, 1, manifestCount, "the terminal manifest remains until bounded manifest cleanup handles it")
	manifestsDeleted, err := s.TimerPeriodBatches().CleanupStorageTerminal(ctx, "crypto", 1)
	require.NoError(t, err)
	require.EqualValues(t, 1, manifestsDeleted)
	deleted, err = s.PeriodSeriesSnapshot().CleanupTerminalBefore(ctx, "crypto", cutoff, 2)
	require.NoError(t, err)
	require.EqualValues(t, 2, deleted, "the snapshot and Storage state are charged to the physical row budget")

	require.NoError(t, s.FetchBatches().Cleanup(ctx, retentionCutoff))
	_, err = s.FetchBatches().Get(ctx, "crypto", batchID)
	require.Error(t, err, "terminal batch becomes eligible after its period manifest is removed")
}

func storageStateForSnapshot(snapshot domain.PeriodSeriesSnapshot, status string) domain.PeriodStorageState {
	return domain.PeriodStorageState{
		Key: snapshot.Key, SeriesHash: snapshot.SeriesHash, ExpectedCount: snapshot.ExpectedCount,
		DeadlineAt: snapshot.Key.PeriodTime.Add(time.Minute), Status: status, ConfirmedAt: snapshot.Key.PeriodTime.Add(2 * time.Minute),
	}
}

func TestListPeriodSeriesSnapshotCleanupCandidatesUsesSpaceScopedStableKeyset(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	period := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	create := func(spaceID, datasetID, frequency string, at time.Time) {
		snapshot := testPeriodSnapshot(at, "BTC-USDT")
		snapshot.Key.SpaceID = spaceID
		snapshot.Key.DatasetID = datasetID
		snapshot.Key.Frequency = frequency
		snapshot.Entries[0].SpaceID = spaceID
		snapshot.Entries[0].DatasetID = datasetID
		snapshot.Entries[0].Frequency = frequency
		_, _, err := s.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, snapshot)
		require.NoError(t, err)
	}
	create("crypto", "bars-b", "1m", period)
	create("crypto", "bars-a", "1h", period)
	create("crypto", "bars-a", "1m", period)
	create("research", "bars-a", "1m", period)
	create("crypto", "bars-new", "1m", period.Add(48*time.Hour))

	first, err := s.PeriodSeriesSnapshot().ListCleanupCandidates(ctx, "crypto", period.Add(24*time.Hour), nil, 2)
	require.NoError(t, err)
	require.Len(t, first.Snapshots, 2)
	require.NotNil(t, first.Next)
	require.Equal(t, []string{"bars-a/1h", "bars-a/1m"}, cleanupSnapshotKeys(first.Snapshots))
	second, err := s.PeriodSeriesSnapshot().ListCleanupCandidates(ctx, "crypto", period.Add(24*time.Hour), first.Next, 2)
	require.NoError(t, err)
	require.Equal(t, []string{"bars-b/1m"}, cleanupSnapshotKeys(second.Snapshots))
	require.NotNil(t, second.Next)
	wrapped, err := s.PeriodSeriesSnapshot().ListCleanupCandidates(ctx, "crypto", period.Add(24*time.Hour), second.Next, 2)
	require.NoError(t, err)
	require.Empty(t, wrapped.Snapshots)
	require.Nil(t, wrapped.Next)
}

func cleanupSnapshotKeys(snapshots []domain.PeriodSeriesSnapshot) []string {
	keys := make([]string, 0, len(snapshots))
	for _, snapshot := range snapshots {
		keys = append(keys, snapshot.Key.DatasetID+"/"+snapshot.Key.Frequency)
	}
	return keys
}

func testPeriodSnapshot(period time.Time, subjectIDs ...string) domain.PeriodSeriesSnapshot {
	rows := make([]domain.PeriodSeriesSnapshotEntry, 0, len(subjectIDs))
	for _, subjectID := range subjectIDs {
		rows = append(rows, domain.PeriodSeriesSnapshotEntry{
			SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period,
			SubjectID: subjectID, Provider: "binance", SourceID: "spot_http", MarketType: "spot",
			ProviderSymbol: stringsToProviderSymbol(subjectID), SeriesTag: "venue:binance|market:spot|source:spot_http",
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		return domain.CanonicalSeriesKey(rows[i].Provider, rows[i].SourceID, rows[i].MarketType, rows[i].SubjectID, rows[i].SeriesTag) < domain.CanonicalSeriesKey(rows[j].Provider, rows[j].SourceID, rows[j].MarketType, rows[j].SubjectID, rows[j].SeriesTag)
	})
	keys := make([]string, 0, len(rows))
	for i := range rows {
		rows[i].SeriesIndex = uint32(i)
		rows[i].SeriesKey = domain.CanonicalSeriesKey(rows[i].Provider, rows[i].SourceID, rows[i].MarketType, rows[i].SubjectID, rows[i].SeriesTag)
		keys = append(keys, rows[i].SeriesKey)
	}
	hash := domain.SeriesSetHash(keys)
	for i := range rows {
		rows[i].SeriesHash = hash
		rows[i].ExpectedCount = len(rows)
	}
	return domain.PeriodSeriesSnapshot{
		Key:        domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period},
		SeriesHash: hash, ExpectedCount: uint32(len(rows)), Entries: rows,
	}
}

func stringsToProviderSymbol(subjectID string) string {
	return strings.ReplaceAll(subjectID, "-", "")
}

func openCollectorStoreAt(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(&Options{Path: path})
	require.NoError(t, err)
	require.NoError(t, s.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return s
}
