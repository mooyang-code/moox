package store

import (
	"context"
	"testing"

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
