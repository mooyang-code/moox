package store

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestTaskSeriesCanonicalIndexAndHash(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	task := domain.CollectionTask{SpaceID: "crypto", TaskID: "task-series", TaskName: "Series", DataType: "kline", CollectParams: `{}`, Enabled: true}
	require.NoError(t, s.Tasks().Create(ctx, task))

	input := []domain.TaskSeries{
		{SubjectID: "ETH-USDT", Provider: "binance", SourceID: "spot_http", MarketType: "spot", ProviderSymbol: "ETHUSDT", SeriesTag: "venue:binance|market:spot|source:spot_http"},
		{SubjectID: "BTC-USDT", Provider: "okx", SourceID: "spot_http", MarketType: "spot", ProviderSymbol: "BTC-USDT", SeriesTag: "venue:okx|market:spot|source:spot_http"},
		{SubjectID: "BTC-USDT", Provider: "binance", SourceID: "spot_http", MarketType: "spot", ProviderSymbol: "BTCUSDT", SeriesTag: "venue:binance|market:spot|source:spot_http"},
		// Duplicate through overlapping Tags must collapse to one normalized series.
		{SubjectID: "ETH-USDT", Provider: "binance", SourceID: "spot_http", MarketType: "spot", ProviderSymbol: "ETHUSDT", SeriesTag: "venue:binance|market:spot|source:spot_http"},
	}
	hash, changed, err := s.Tasks().ReplaceTaskSeries(ctx, "crypto", "task-series", input)
	require.NoError(t, err)
	require.True(t, changed)
	require.NotEmpty(t, hash)
	rows, err := s.Tasks().ListTaskSeries(ctx, "crypto", "task-series")
	require.NoError(t, err)
	require.Len(t, rows, 3)
	for i := range rows {
		require.Equal(t, uint32(i), rows[i].SeriesIndex)
	}
	require.NotEqual(t, rows[0].SeriesKey, rows[1].SeriesKey)

	// Input order does not change the canonical index/hash.
	reordered := []domain.TaskSeries{input[2], input[0], input[1]}
	hash2, changed, err := s.Tasks().ReplaceTaskSeries(ctx, "crypto", "task-series", reordered)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, hash, hash2)
	rows2, err := s.Tasks().ListTaskSeries(ctx, "crypto", "task-series")
	require.NoError(t, err)
	require.Equal(t, rows, rows2)

	// A real membership change produces a new hash and dense reindexing.
	input = append(input, domain.TaskSeries{SubjectID: "SOL-USDT", Provider: "binance", SourceID: "spot_http", MarketType: "spot", ProviderSymbol: "SOLUSDT", SeriesTag: "venue:binance|market:spot|source:spot_http"})
	hash3, changed, err := s.Tasks().ReplaceTaskSeries(ctx, "crypto", "task-series", input)
	require.NoError(t, err)
	require.True(t, changed)
	require.NotEqual(t, hash, hash3)
	rows3, err := s.Tasks().ListTaskSeries(ctx, "crypto", "task-series")
	require.NoError(t, err)
	require.Len(t, rows3, 4)
	for i := range rows3 {
		require.Equal(t, uint32(i), rows3[i].SeriesIndex)
	}
	taskAfter, err := s.Tasks().GetByTaskID(ctx, "crypto", "task-series")
	require.NoError(t, err)
	require.Equal(t, hash3, taskAfter.SeriesHash)
}

func TestTaskRepositoryReadSingleTaskSeriesIsBoundedAndFailsClosed(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	require.NoError(t, s.Tasks().Create(ctx, domain.CollectionTask{SpaceID: "crypto", TaskID: "single", TaskName: "Single", DataType: "kline", CollectParams: `{}`, Enabled: true}))
	require.NoError(t, s.Tasks().Create(ctx, domain.CollectionTask{SpaceID: "crypto", TaskID: "multiple", TaskName: "Multiple", DataType: "kline", CollectParams: `{}`, Enabled: true}))
	_, _, err := s.Tasks().ReplaceTaskSeries(ctx, "crypto", "single", []domain.TaskSeries{{SubjectID: "BTC-USDT", Provider: "binance", SourceID: "spot_http", MarketType: "spot", ProviderSymbol: "BTCUSDT", SeriesTag: "venue:binance"}})
	require.NoError(t, err)
	_, _, err = s.Tasks().ReplaceTaskSeries(ctx, "crypto", "multiple", []domain.TaskSeries{
		{SubjectID: "BTC-USDT", Provider: "binance", SourceID: "spot_http", MarketType: "spot", ProviderSymbol: "BTCUSDT", SeriesTag: "venue:binance"},
		{SubjectID: "ETH-USDT", Provider: "binance", SourceID: "spot_http", MarketType: "spot", ProviderSymbol: "ETHUSDT", SeriesTag: "venue:binance"},
	})
	require.NoError(t, err)

	row, ok, err := s.Tasks().ReadSingleTaskSeries(ctx, "crypto", "single")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "BTC-USDT", row.SubjectID)

	row, ok, err = s.Tasks().ReadSingleTaskSeries(ctx, "crypto", "multiple")
	require.NoError(t, err)
	require.False(t, ok)
	require.Nil(t, row)

	row, ok, err = s.Tasks().ReadSingleTaskSeries(ctx, "crypto", "missing")
	require.NoError(t, err)
	require.False(t, ok)
	require.Nil(t, row)
}

func TestPeriodSeriesSnapshotSurvivesCurrentTaskSeriesChange(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	require.NoError(t, s.Tasks().Create(ctx, domain.CollectionTask{SpaceID: "crypto", TaskID: "task-period", TaskName: "Period", DataType: "kline", CollectParams: `{}`, Enabled: true}))

	initial := []domain.TaskSeries{
		{SubjectID: "BTC-USDT", Provider: "binance", SourceID: "spot_http", MarketType: "spot", ProviderSymbol: "BTCUSDT", SeriesTag: "venue:binance|market:spot|source:spot_http"},
		{SubjectID: "ETH-USDT", Provider: "binance", SourceID: "spot_http", MarketType: "spot", ProviderSymbol: "ETHUSDT", SeriesTag: "venue:binance|market:spot|source:spot_http"},
		{SubjectID: "BTC-USDT", Provider: "okx", SourceID: "spot_http", MarketType: "spot", ProviderSymbol: "BTC-USDT", SeriesTag: "venue:okx|market:spot|source:spot_http"},
	}
	currentHash, _, err := s.Tasks().ReplaceTaskSeries(ctx, "crypto", "task-period", initial)
	require.NoError(t, err)
	period := time.Date(2026, 9, 29, 8, 10, 0, 0, time.UTC).Unix()
	snapshot := periodSeriesSnapshotForTaskSeries("crypto", "bars", "1m", period, initial)
	saved, created, err := s.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, snapshot)
	require.NoError(t, err)
	require.True(t, created)
	require.Len(t, saved.Entries, 3)
	wantKeys := []string{
		"binance\x00spot_http\x00spot\x00btc-usdt\x00venue:binance|market:spot|source:spot_http",
		"binance\x00spot_http\x00spot\x00eth-usdt\x00venue:binance|market:spot|source:spot_http",
		"okx\x00spot_http\x00spot\x00btc-usdt\x00venue:okx|market:spot|source:spot_http",
	}
	require.Equal(t, "3652f82a6c767f05a7816ea0cc42c817772239f4c9af147026b1fda0b93fe954", currentHash)
	universe := make(map[string]struct{})
	require.Equal(t, currentHash, saved.SeriesHash)
	require.Equal(t, uint32(3), saved.ExpectedCount)
	for index, row := range saved.Entries {
		require.Equal(t, uint32(index), row.SeriesIndex)
		require.Equal(t, wantKeys[index], row.SeriesKey)
		require.Equal(t, currentHash, row.SeriesHash)
		require.Equal(t, 3, row.ExpectedCount)
		universe[row.SubjectID] = struct{}{}
	}
	require.Len(t, universe, 2, "two providers for one Subject form separate series, not separate universe members")

	updatedCurrent := []domain.TaskSeries{initial[0]}
	updatedHash, _, err := s.Tasks().ReplaceTaskSeries(ctx, "crypto", "task-period", updatedCurrent)
	require.NoError(t, err)
	require.NotEqual(t, currentHash, updatedHash)

	loaded, found, err := s.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, saved.Key)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, saved, loaded)
	require.Equal(t, currentHash, loaded.SeriesHash)
	require.Equal(t, uint32(3), loaded.ExpectedCount)
	require.Equal(t, []string{"BTC-USDT", "ETH-USDT", "BTC-USDT"}, periodSeriesSubjects(loaded.Entries))

	nextPeriod := period + 60
	nextSnapshot, created, err := s.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, periodSeriesSnapshotForTaskSeries("crypto", "bars", "1m", nextPeriod, updatedCurrent))
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, []string{"BTC-USDT"}, periodSeriesSubjects(nextSnapshot.Entries))
}

func TestCreatePeriodSeriesSnapshotIfAbsentIsConcurrentIdempotentAndRejectsConflict(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	period := time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC).Unix()
	input := periodSeriesSnapshotForTaskSeries("crypto", "bars", "1h", period, []domain.TaskSeries{
		{SubjectID: "BTC-USDT", Provider: "binance", SourceID: "spot_http", MarketType: "spot", ProviderSymbol: "BTCUSDT", SeriesTag: "binance-spot"},
		{SubjectID: "ETH-USDT", Provider: "binance", SourceID: "spot_http", MarketType: "spot", ProviderSymbol: "ETHUSDT", SeriesTag: "binance-spot"},
	})

	const callers = 12
	var wg sync.WaitGroup
	var resultMu sync.Mutex
	createdCount := 0
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			snapshot, created, err := s.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, input)
			if err == nil && len(snapshot.Entries) != 2 {
				err = fmt.Errorf("concurrent snapshot has %d rows, want 2", len(snapshot.Entries))
			}
			if err == nil && created {
				resultMu.Lock()
				createdCount++
				resultMu.Unlock()
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, 1, createdCount)

	changed := periodSeriesSnapshotForTaskSeries("crypto", "bars", "1h", period, []domain.TaskSeries{
		{SubjectID: "BTC-USDT", Provider: "binance", SourceID: "spot_http", MarketType: "spot", ProviderSymbol: "BTCUSDT", SeriesTag: "binance-spot"},
	})
	_, created, err := s.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, changed)
	require.False(t, created)
	require.ErrorIs(t, err, ErrPeriodSeriesSnapshotConflict)
}

func periodSeriesSnapshotForTaskSeries(spaceID, datasetID, frequency string, period int64, input []domain.TaskSeries) domain.PeriodSeriesSnapshot {
	byKey := make(map[string]domain.TaskSeries, len(input))
	for _, item := range input {
		key := domain.CanonicalSeriesKey(item.Provider, item.SourceID, item.MarketType, item.SubjectID, item.SeriesTag)
		byKey[key] = item
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	hash := domain.SeriesSetHash(keys)
	rows := make([]domain.PeriodSeriesSnapshotEntry, 0, len(keys))
	periodAt := time.Unix(period, 0).UTC()
	for index, key := range keys {
		item := byKey[key]
		rows = append(rows, domain.PeriodSeriesSnapshotEntry{
			SpaceID: spaceID, DatasetID: datasetID, Frequency: frequency, PeriodTime: periodAt, SeriesIndex: uint32(index),
			SeriesKey: key, SubjectID: item.SubjectID, Provider: item.Provider, SourceID: item.SourceID, MarketType: item.MarketType,
			ProviderSymbol: item.ProviderSymbol, SeriesTag: item.SeriesTag, SeriesHash: hash, ExpectedCount: len(keys),
		})
	}
	return domain.PeriodSeriesSnapshot{
		Key:        domain.PeriodKey{SpaceID: spaceID, DatasetID: datasetID, Frequency: frequency, PeriodTime: periodAt},
		SeriesHash: hash, ExpectedCount: uint32(len(rows)), Entries: rows,
	}
}

func periodSeriesSubjects(rows []domain.PeriodSeriesSnapshotEntry) []string {
	subjects := make([]string, 0, len(rows))
	for _, row := range rows {
		subjects = append(subjects, row.SubjectID)
	}
	return subjects
}
