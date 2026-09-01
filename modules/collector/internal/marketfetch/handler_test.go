package marketfetch

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/marketfetchpb"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

type timerMetricsReporter struct{ calls atomic.Int32 }

func (r *timerMetricsReporter) Handle(context.Context) error {
	r.calls.Add(1)
	return nil
}

type timerHandlerStorage struct{}

func (timerHandlerStorage) UpsertFields(context.Context, []*storagepb.RowFieldUpsert) error {
	return nil
}

func (timerHandlerStorage) RegisterDataSubject(context.Context, *storagepb.RegisterDataSubjectReq) error {
	return nil
}

func TestHandleTimerAtReportsMetricsForTimerExecution(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", "stock_cn")
	t.Setenv("MOOX_MARKET_FETCH_PROVIDER", "stock_cn_multi")
	t.Setenv("MOOX_MARKET_FETCH_MARKET_TYPE", "equity")
	t.Setenv("MOOX_MARKET_FETCH_DATASET_ID", "stock_cn_kline")
	t.Setenv("MOOX_MARKET_FETCH_FREQUENCY", "1m")
	t.Setenv("MOOX_MARKET_FETCH_SUBJECTS", "600000.XSHG")
	t.Setenv("MOOX_MARKET_FETCH_SYMBOLS_JSON", `{"600000.XSHG":"sh600000"}`)
	t.Setenv("MOOX_MARKET_FETCH_GROUP_ID", "3")
	t.Setenv("MOOX_MARKET_FETCH_GROUP_COUNT", "4")
	t.Setenv("MOOX_STORAGE_RPC_GATEWAY_TARGET", "ip://storage:11003")
	reporter := &timerMetricsReporter{}
	handler := &Handler{
		NewStorage: func(string, string, string) (Storage, error) { return timerHandlerStorage{}, nil },
		Execute: func(_ context.Context, req Request, _ Storage) (*marketfetchpb.MarketFetchBatchCompleted, error) {
			require.Equal(t, domain.BatchKindRealtime, req.BatchKind)
			require.Equal(t, 3, req.GroupID)
			require.Equal(t, 4, req.GroupCount)
			return &marketfetchpb.MarketFetchBatchCompleted{Status: "succeeded"}, nil
		},
		MetricsReporter: reporter,
	}

	response, err := handler.HandleTimerAt(context.Background(), "request-1", "function-1", time.Date(2026, 8, 30, 3, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.True(t, response.Success)
	require.Equal(t, int32(1), reporter.calls.Load())
}

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
		NewStorage: func(string, string, string) (Storage, error) { return storage, nil },
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
	response, err := handler.handleRequest(context.Background(), req, "storage", false)
	require.NoError(t, err)
	require.True(t, response.Success)
	require.Equal(t, 1.0, testutil.ToFloat64(metrics.feedResults.WithLabelValues(StockCNSpaceID, StockCNRouteID, "sina", "kline", "3", "realtime", "success")))
	require.Len(t, storage.rows, 1)
}
