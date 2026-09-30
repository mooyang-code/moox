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

func TestPeriodSeriesRosterSurvivesCurrentTaskSeriesChange(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	require.NoError(t, s.Tasks().Create(ctx, domain.CollectionTask{SpaceID: "crypto", TaskID: "task-period", TaskName: "Period", DataType: "kline", CollectParams: `{}`, Enabled: true}))

	initial := []domain.TaskSeries{
		{SubjectID: "BTC-USDT", Provider: "binance", SourceID: "spot_http", MarketType: "spot", ProviderSymbol: "BTCUSDT", SeriesTag: "venue:binance|market:spot|source:spot_http"},
		{SubjectID: "ETH-USDT", Provider: "binance", SourceID: "spot_http", MarketType: "spot", ProviderSymbol: "ETHUSDT", SeriesTag: "venue:binance|market:spot|source:spot_http"},
	}
	currentHash, _, err := s.Tasks().ReplaceTaskSeries(ctx, "crypto", "task-period", initial)
	require.NoError(t, err)
	period := time.Date(2026, 9, 29, 8, 10, 0, 0, time.UTC).Unix()
	roster := periodSeriesRows("crypto", "bars", "1m", period, initial)
	saved, created, err := s.PeriodSeries().CreatePeriodSeriesIfAbsent(ctx, roster)
	require.NoError(t, err)
	require.True(t, created)
	require.Len(t, saved, 2)

	updatedCurrent := []domain.TaskSeries{initial[0]}
	updatedHash, _, err := s.Tasks().ReplaceTaskSeries(ctx, "crypto", "task-period", updatedCurrent)
	require.NoError(t, err)
	require.NotEqual(t, currentHash, updatedHash)

	loaded, err := s.PeriodSeries().GetPeriodSeries(ctx, domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: time.Unix(period, 0).UTC()})
	require.NoError(t, err)
	require.Equal(t, saved, loaded)
	require.Equal(t, currentHash, loaded[0].SeriesHash)
	require.Equal(t, 2, loaded[0].ExpectedCount)
	require.Equal(t, []string{"BTC-USDT", "ETH-USDT"}, periodSeriesSubjects(loaded))

	nextPeriod := period + 60
	nextRoster, created, err := s.PeriodSeries().CreatePeriodSeriesIfAbsent(ctx, periodSeriesRows("crypto", "bars", "1m", nextPeriod, updatedCurrent))
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, []string{"BTC-USDT"}, periodSeriesSubjects(nextRoster))
}

func TestCreatePeriodSeriesIfAbsentIsConcurrentIdempotentAndRejectsConflict(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	period := time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC).Unix()
	input := periodSeriesRows("crypto", "bars", "1h", period, []domain.TaskSeries{
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
			rows, created, err := s.PeriodSeries().CreatePeriodSeriesIfAbsent(ctx, input)
			if err == nil && len(rows) != 2 {
				err = fmt.Errorf("concurrent roster has %d rows, want 2", len(rows))
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

	changed := periodSeriesRows("crypto", "bars", "1h", period, []domain.TaskSeries{
		{SubjectID: "BTC-USDT", Provider: "binance", SourceID: "spot_http", MarketType: "spot", ProviderSymbol: "BTCUSDT", SeriesTag: "binance-spot"},
	})
	_, created, err := s.PeriodSeries().CreatePeriodSeriesIfAbsent(ctx, changed)
	require.False(t, created)
	require.ErrorIs(t, err, ErrPeriodSeriesConflict)
}

func periodSeriesRows(spaceID, datasetID, frequency string, period int64, input []domain.TaskSeries) []domain.TaskPeriodSeries {
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
	rows := make([]domain.TaskPeriodSeries, 0, len(keys))
	periodAt := time.Unix(period, 0).UTC()
	for index, key := range keys {
		item := byKey[key]
		rows = append(rows, domain.TaskPeriodSeries{
			SpaceID: spaceID, DatasetID: datasetID, Frequency: frequency, PeriodTime: periodAt, SeriesIndex: uint32(index),
			SeriesKey: key, SubjectID: item.SubjectID, Provider: item.Provider, SourceID: item.SourceID, MarketType: item.MarketType,
			ProviderSymbol: item.ProviderSymbol, SeriesTag: item.SeriesTag, SeriesHash: hash, ExpectedCount: len(keys),
		})
	}
	return rows
}

func periodSeriesSubjects(rows []domain.TaskPeriodSeries) []string {
	subjects := make([]string, 0, len(rows))
	for _, row := range rows {
		subjects = append(subjects, row.SubjectID)
	}
	return subjects
}
