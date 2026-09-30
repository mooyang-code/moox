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

func TestPeriodSeriesRosterIsImmutableAndNextPeriodUsesNewMembership(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	period := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

	first, created, err := s.PeriodSeries().CreatePeriodSeriesIfAbsent(ctx, testPeriodRoster(period, "BTC-USDT", "ETH-USDT"))
	require.NoError(t, err)
	require.True(t, created)
	require.Len(t, first, 2)

	changedMembership := testPeriodRoster(period, "BTC-USDT")
	_, created, err = s.PeriodSeries().CreatePeriodSeriesIfAbsent(ctx, changedMembership)
	require.ErrorIs(t, err, ErrPeriodSeriesConflict)
	require.False(t, created)

	stored, err := s.PeriodSeries().GetPeriodSeries(ctx, domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period})
	require.NoError(t, err)
	require.Equal(t, first, stored, "membership refresh must not change the existing period roster")
	require.Equal(t, first[0].SeriesHash, stored[0].SeriesHash)
	require.Equal(t, uint32(0), stored[0].SeriesIndex)
	require.Equal(t, uint32(1), stored[1].SeriesIndex)
	require.Equal(t, 2, stored[0].ExpectedCount)

	nextPeriod := period.Add(time.Minute)
	next, nextCreated, err := s.PeriodSeries().CreatePeriodSeriesIfAbsent(ctx, testPeriodRoster(nextPeriod, "BTC-USDT"))
	require.NoError(t, err)
	require.True(t, nextCreated)
	require.Len(t, next, 1)
	require.Equal(t, "BTC-USDT", next[0].SubjectID)
	require.Equal(t, 1, next[0].ExpectedCount)
}

func TestCreatePeriodSeriesIfAbsentIsIdempotentForConcurrentSameRoster(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	firstStore := openCollectorStoreAt(t, dbPath)
	secondStore := openCollectorStoreAt(t, dbPath)
	period := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	input := testPeriodRoster(period, "BTC-USDT", "ETH-USDT", "SOL-USDT")

	start := make(chan struct{})
	results := make(chan []domain.TaskPeriodSeries, 2)
	errs := make(chan error, 2)
	created := make(chan bool, 2)
	var wg sync.WaitGroup
	for _, repo := range []*Store{firstStore, secondStore} {
		wg.Add(1)
		go func(repo *Store) {
			defer wg.Done()
			<-start
			rows, wasCreated, err := repo.PeriodSeries().CreatePeriodSeriesIfAbsent(context.Background(), input)
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
	rows := make([][]domain.TaskPeriodSeries, 0, 2)
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

	stored, err := firstStore.PeriodSeries().GetPeriodSeries(context.Background(), domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period})
	require.NoError(t, err)
	require.Equal(t, rows[0], stored)
}

func TestCreatePeriodSeriesIfAbsentRejectsDifferentRoster(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	period := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	first := testPeriodRoster(period, "BTC-USDT", "ETH-USDT")
	persistedFirst, created, err := s.PeriodSeries().CreatePeriodSeriesIfAbsent(ctx, first)
	require.NoError(t, err)
	require.True(t, created)

	_, created, err = s.PeriodSeries().CreatePeriodSeriesIfAbsent(ctx, testPeriodRoster(period, "BTC-USDT", "SOL-USDT"))
	require.ErrorIs(t, err, ErrPeriodSeriesConflict)
	require.False(t, created)
	stored, err := s.PeriodSeries().GetPeriodSeries(ctx, domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period})
	require.NoError(t, err)
	require.Equal(t, persistedFirst, stored)
}

func TestCreatePeriodSeriesIfAbsentValidatesDenseIndexHashAndCount(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func([]domain.TaskPeriodSeries) []domain.TaskPeriodSeries
	}{
		{name: "dense index", mutate: func(rows []domain.TaskPeriodSeries) []domain.TaskPeriodSeries { rows[1].SeriesIndex = 2; return rows }},
		{name: "series hash", mutate: func(rows []domain.TaskPeriodSeries) []domain.TaskPeriodSeries {
			rows[0].SeriesHash = "wrong"
			rows[1].SeriesHash = "wrong"
			return rows
		}},
		{name: "expected count", mutate: func(rows []domain.TaskPeriodSeries) []domain.TaskPeriodSeries {
			rows[0].ExpectedCount++
			rows[1].ExpectedCount++
			return rows
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := newCollectorStore(t)
			rows := test.mutate(testPeriodRoster(time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC), "BTC-USDT", "ETH-USDT"))
			_, _, err := s.PeriodSeries().CreatePeriodSeriesIfAbsent(context.Background(), rows)
			require.Error(t, err)
		})
	}
}

func TestCleanupPeriodSeriesDeletesOnlyOldQuiescentPeriodsAndIsBounded(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	oldPeriod := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	recentPeriod := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	_, _, err := s.PeriodSeries().CreatePeriodSeriesIfAbsent(ctx, testPeriodRoster(oldPeriod, "BTC-USDT", "ETH-USDT"))
	require.NoError(t, err)
	_, _, err = s.PeriodSeries().CreatePeriodSeriesIfAbsent(ctx, testPeriodRoster(recentPeriod, "BTC-USDT"))
	require.NoError(t, err)

	// An old terminal failure that has not reached Storage must retain its roster.
	oldUnreported := oldPeriod.Add(time.Minute)
	_, _, err = s.PeriodSeries().CreatePeriodSeriesIfAbsent(ctx, testPeriodRoster(oldUnreported, "SOL-USDT"))
	require.NoError(t, err)
	require.NoError(t, s.FetchRetries().Upsert(ctx, &domain.RetryItem{
		SpaceID: "crypto", RetryKey: "unreported-period-failure", SubjectID: "SOL-USDT", Frequency: "1m",
		TargetDataTime: oldUnreported, Status: "permanent_failed", FailureTargetsJSON: "[]", CreateTime: oldUnreported,
	}))
	require.NoError(t, s.db.Exec(`INSERT INTO t_period_readiness (c_space_id, c_dataset_id, c_frequency, c_work_type, c_period_time, c_deadline_at, c_status, c_report_state, c_collected_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, "crypto", "bars", "1m", "collection", oldPeriod, oldPeriod.Add(time.Minute), domain.PeriodStatusComplete, domain.PeriodReportReported, oldPeriod.Add(time.Hour)).Error)

	deleted, err := s.PeriodSeries().CleanupReportedBefore(ctx, oldPeriod.Add(24*time.Hour), 1)
	require.NoError(t, err)
	require.EqualValues(t, 2, deleted, "the row limit applies to periods so a roster is never partially deleted")

	rows, err := s.PeriodSeries().GetPeriodSeries(ctx, domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: oldPeriod})
	require.NoError(t, err)
	require.Empty(t, rows)
	rows, err = s.PeriodSeries().GetPeriodSeries(ctx, domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: oldUnreported})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	rows, err = s.PeriodSeries().GetPeriodSeries(ctx, domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: recentPeriod})
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

func testPeriodRoster(period time.Time, subjectIDs ...string) []domain.TaskPeriodSeries {
	rows := make([]domain.TaskPeriodSeries, 0, len(subjectIDs))
	for _, subjectID := range subjectIDs {
		rows = append(rows, domain.TaskPeriodSeries{
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
	return rows
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
