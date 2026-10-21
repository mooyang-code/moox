package marketfetch

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestHandlerWiresMetricsIntoCommonStockKlinePipeline(t *testing.T) {
	now := time.Date(2026, 8, 28, 7, 5, 0, 0, time.UTC)
	bar := marketdata.NormalizedKline{
		SubjectID: "600000.XSHG", ProviderID: "sina", ProviderSymbol: "sh600000", Frequency: "1m",
		BarStart: now.Add(-6 * time.Minute), BarEnd: now.Add(-5 * time.Minute), Open: 9, High: 9.1, Low: 8.9, Close: 9.05,
		VolumeShares: 1000, AmountCNY: 9050, ProviderTimestamp: now.Add(-5 * time.Minute), FetchedAt: now, RequestID: "handler-metrics",
	}
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(pipelineProvider{id: "sina", rows: []marketdata.NormalizedKline{bar}}))
	require.NoError(t, registry.Register(pipelineProvider{id: "tencent", err: marketdata.ErrNoClosedBar}))
	router, err := marketdata.NewRouter(registry, 2, pipelineClock{now}, func(time.Duration) {})
	require.NoError(t, err)
	storage := &pipelineStorage{}
	pipeline := &KlinePipeline{Router: router, CandidateChain: []string{"sina", "tencent"}, MarketID: StockCNSpaceID, SpaceID: StockCNSpaceID, DatasetID: StockCNDatasetID, Now: func() time.Time { return now }}
	metrics := NewMetrics(prometheus.NewRegistry())
	handler := &Handler{
		NewStorage: func(string, string) (Storage, error) { return storage, nil },
		NewStockKlinePipeline: func(s Storage) (*KlinePipeline, error) {
			pipeline.Storage = s
			return pipeline, nil
		},
		Metrics: metrics,
		Now:     func() time.Time { return now },
	}
	req := Request{
		BatchID: "handler-metrics-batch", BatchKind: domain.BatchKindRealtime, SpaceID: StockCNSpaceID, DatasetID: StockCNDatasetID,
		Frequency: "1m", Provider: "sina", MarketType: "equity", RequestID: "handler-metrics", GroupID: 3, GroupCount: 4,
		Items: []domain.CollectionItem{{SubjectID: bar.SubjectID, Symbol: bar.ProviderSymbol, Provider: "sina", MarketType: "equity", DataType: "kline", DatasetID: StockCNDatasetID, Frequency: "1m", BarLimit: 1}},
	}
	response, err := handler.handleRequest(context.Background(), req)
	require.NoError(t, err)
	require.True(t, response.Success)
	require.Equal(t, 1.0, testutil.ToFloat64(metrics.feedResults.WithLabelValues(StockCNSpaceID, StockCNRouteID, "sina", "kline", "3", "realtime", "success")))
	require.Len(t, storage.rows, 1)
}

func TestHandleRequestUsesCryptoSwapPipeline(t *testing.T) {
	now := time.Date(2026, 9, 22, 7, 40, 0, 0, time.UTC)
	bar := marketdata.NormalizedKline{
		SubjectID: "BTC-USDT", ProviderID: "binance", SourceID: "swap_http", ProviderSymbol: "BTCUSDT", Frequency: "1m",
		BarStart: now.Add(-2 * time.Minute), BarEnd: now.Add(-time.Minute), Open: 1, High: 1, Low: 1, Close: 1,
		ProviderTimestamp: now.Add(-time.Minute), FetchedAt: now, RequestID: "swap-pipeline",
	}
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(pipelineProvider{id: "binance", sourceID: "swap_http", rows: []marketdata.NormalizedKline{bar}}))
	router, err := marketdata.NewRouter(registry, 2, pipelineClock{now}, func(time.Duration) {})
	require.NoError(t, err)
	storage := &pipelineStorage{}
	var product marketdata.InstrumentType
	var gotLimits *FeedLimits
	handler := &Handler{
		NewStorage: func(string, string) (Storage, error) { return storage, nil },
		NewCryptoKlinePipeline: func(s Storage, got marketdata.InstrumentType, limits *FeedLimits) (*KlinePipeline, error) {
			product, gotLimits = got, limits
			return &KlinePipeline{
				Router: router, Storage: s, CandidateChain: []string{"binance"}, MarketID: "crypto",
				InstrumentType: got, DatasetID: "dataset_binance_kline_1m",
				SourceID: "swap_http", Now: func() time.Time { return now },
			}, nil
		},
		NewMarketKlinePipeline: func(Storage, string, marketdata.InstrumentType, string, string) (*KlinePipeline, error) {
			t.Fatal("crypto swap must not use the generic market pipeline")
			return nil, nil
		},
		Now: func() time.Time { return now },
	}
	req := Request{
		BatchID: "swap-pipeline", BatchKind: domain.BatchKindRealtime, SpaceID: "crypto", MarketID: "crypto",
		DatasetID: "dataset_binance_kline_1m", Frequency: "1m", Provider: "binance", SourceID: "swap_http",
		MarketType: "swap", InstrumentType: "swap", RequestID: "swap-pipeline", FetchLimits: &FeedLimits{RPS: 40, Burst: 40, FeedConcurrency: 16, RequestTimeoutMS: 3000},
		Items: []domain.CollectionItem{{SubjectID: "BTC-USDT", Symbol: "BTCUSDT", Provider: "binance", SourceID: "swap_http", MarketType: "swap", DataType: "kline", DatasetID: "dataset_binance_kline_1m", Frequency: "1m", BarLimit: 1}},
	}
	response, err := handler.handleRequest(context.Background(), req)
	require.NoError(t, err)
	require.True(t, response.Success)
	require.Equal(t, marketdata.InstrumentSwap, product)
	require.Equal(t, &FeedLimits{RPS: 40, Burst: 40, FeedConcurrency: 16, RequestTimeoutMS: 3000}, gotLimits, "计划的限流随请求交给数据源（常驻本地执行端不读进程环境）")
	require.Len(t, storage.rows, 1)
}

func TestHandleRequestAlignsStaleSpotSourceOntoSwapPipeline(t *testing.T) {
	now := time.Date(2026, 9, 22, 7, 40, 0, 0, time.UTC)
	bar := marketdata.NormalizedKline{
		SubjectID: "ETH-USDT", ProviderID: "binance", SourceID: "swap_http", ProviderSymbol: "ETHUSDT", Frequency: "1m",
		BarStart: now.Add(-2 * time.Minute), BarEnd: now.Add(-time.Minute), Open: 1, High: 1, Low: 1, Close: 1,
		ProviderTimestamp: now.Add(-time.Minute), FetchedAt: now, RequestID: "stale-source",
	}
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(pipelineProvider{id: "binance", sourceID: "swap_http", rows: []marketdata.NormalizedKline{bar}}))
	router, err := marketdata.NewRouter(registry, 2, pipelineClock{now}, func(time.Duration) {})
	require.NoError(t, err)
	storage := &pipelineStorage{}
	handler := &Handler{
		NewStorage: func(string, string) (Storage, error) { return storage, nil },
		NewCryptoKlinePipeline: func(s Storage, got marketdata.InstrumentType, _ *FeedLimits) (*KlinePipeline, error) {
			return &KlinePipeline{
				Router: router, Storage: s, CandidateChain: []string{"binance"}, MarketID: "crypto",
				InstrumentType: got, DatasetID: "dataset_binance_kline_1m",
				SourceID: "swap_http", Now: func() time.Time { return now },
			}, nil
		},
		Now: func() time.Time { return now },
	}
	req := Request{
		BatchID: "stale-source", BatchKind: domain.BatchKindRealtime, SpaceID: "crypto", MarketID: "crypto",
		DatasetID: "dataset_binance_kline_1m", Frequency: "1m", Provider: "binance", SourceID: "spot_http",
		MarketType: "swap", InstrumentType: "swap", RequestID: "stale-source",
		Items: []domain.CollectionItem{{SubjectID: "ETH-USDT", Symbol: "ETHUSDT", Provider: "binance", SourceID: "spot_http", MarketType: "swap", DataType: "kline", DatasetID: "dataset_binance_kline_1m", Frequency: "1m", BarLimit: 1}},
	}
	response, err := handler.handleRequest(context.Background(), req)
	require.NoError(t, err)
	require.True(t, response.Success, "%+v", response)
	require.Len(t, storage.rows, 1)
}
