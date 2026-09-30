package store

import (
	"context"
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

func TestCleanupPeriodSeriesSnapshotsDeletesOnlyOldQuiescentPeriodsAndIsBounded(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	oldPeriod := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	recentPeriod := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	_, _, err := s.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, testPeriodSnapshot(oldPeriod, "BTC-USDT", "ETH-USDT"))
	require.NoError(t, err)
	_, _, err = s.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, testPeriodSnapshot(recentPeriod, "BTC-USDT"))
	require.NoError(t, err)

	// An old terminal failure that has not reached Storage must retain its snapshot.
	oldUnreported := oldPeriod.Add(time.Minute)
	_, _, err = s.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, testPeriodSnapshot(oldUnreported, "SOL-USDT"))
	require.NoError(t, err)
	require.NoError(t, s.FetchRetries().Upsert(ctx, &domain.RetryItem{
		SpaceID: "crypto", RetryKey: "unreported-period-failure", SubjectID: "SOL-USDT", Frequency: "1m",
		TargetDataTime: oldUnreported, Status: "permanent_failed", FailureTargetsJSON: "[]", CreateTime: oldUnreported,
	}))
	require.NoError(t, s.db.Exec(`INSERT INTO t_period_readiness (c_space_id, c_dataset_id, c_frequency, c_work_type, c_period_time, c_deadline_at, c_status, c_report_state, c_collected_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, "crypto", "bars", "1m", "collection", oldPeriod, oldPeriod.Add(time.Minute), domain.PeriodStatusComplete, domain.PeriodReportReported, oldPeriod.Add(time.Hour)).Error)

	deleted, err := s.PeriodSeriesSnapshot().CleanupReportedBefore(ctx, oldPeriod.Add(24*time.Hour), 1)
	require.NoError(t, err)
	require.EqualValues(t, 2, deleted, "the row limit applies to periods so a snapshot is never partially deleted")

	snapshot, found, err := s.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: oldPeriod})
	require.NoError(t, err)
	require.False(t, found)
	require.Empty(t, snapshot.Entries)
	snapshot, found, err = s.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: oldUnreported})
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, snapshot.Entries, 1)
	snapshot, found, err = s.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: recentPeriod})
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, snapshot.Entries, 1)
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
