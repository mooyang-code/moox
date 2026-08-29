package marketfetch

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	stockmarket "github.com/mooyang-code/moox/modules/collector/internal/markets/stockcn"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

type pipelineClock struct{ now time.Time }

func (c pipelineClock) Now() time.Time { return c.now }

type pipelineProvider struct {
	id   string
	rows []marketdata.NormalizedKline
	err  error
}

func TestStockCNShouldCollectMinuteHonorsSessionsAndClosedDays(t *testing.T) {
	calendar, err := stockmarket.LoadCalendar("../../config/markets/stock_cn/calendar.yaml")
	require.NoError(t, err)
	location := calendar.Location()
	openMinute := time.Date(2026, 8, 28, 9, 31, 0, 0, location)
	shouldCollect, err := stockCNShouldCollectMinute(calendar, openMinute)
	require.NoError(t, err)
	require.True(t, shouldCollect)
	noon := time.Date(2026, 8, 28, 12, 0, 0, 0, location)
	shouldCollect, err = stockCNShouldCollectMinute(calendar, noon)
	require.NoError(t, err)
	require.False(t, shouldCollect)
	weekend := time.Date(2026, 8, 29, 10, 0, 0, 0, location)
	shouldCollect, err = stockCNShouldCollectMinute(calendar, weekend)
	require.NoError(t, err)
	require.False(t, shouldCollect)
}

func (p pipelineProvider) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{ID: p.id, DisplayName: p.id, Hosts: []string{p.id + ".test"}}
}

func (p pipelineProvider) KlineSpec() marketdata.KlineSpec {
	return marketdata.KlineSpec{Markets: []string{"stock_cn"}, Exchanges: []string{"XSHG"}, Frequencies: []string{"1m"}, CompleteOHLCV: true, HasAmount: true, MaxBarsPerRequest: 3, TimestampMode: marketdata.TimestampModeOpen, RateLimit: marketdata.RateLimitPolicy{RequestsPerSecond: 100, Burst: 3, MaxConcurrent: 1, Cooldown: time.Second, RequestTimeout: time.Second}}
}

func (p pipelineProvider) FetchKlines(context.Context, marketdata.KlineRequest) ([]marketdata.NormalizedKline, error) {
	return p.rows, p.err
}

type pipelineStorage struct {
	rows          []*storagepb.RowFieldUpsert
	sourceEventID string
	writes        int
}

func (s *pipelineStorage) UpsertFields(context.Context, []*storagepb.RowFieldUpsert) error {
	return nil
}
func (s *pipelineStorage) RegisterDataSubject(context.Context, *storagepb.RegisterDataSubjectReq) error {
	return nil
}
func (s *pipelineStorage) UpsertFieldsWithSource(_ context.Context, rows []*storagepb.RowFieldUpsert, source string) error {
	s.rows = rows
	s.sourceEventID = source
	s.writes++
	return nil
}

func TestKlinePipelineWritesOneCompleteStockDatasetBatch(t *testing.T) {
	now := time.Date(2026, 8, 29, 2, 0, 0, 0, time.UTC)
	bar := marketdata.NormalizedKline{SubjectID: "600000.XSHG", ProviderID: "sina", ProviderSymbol: "sh600000", Frequency: "1m", BarStart: time.Date(2026, 8, 28, 6, 59, 0, 0, time.UTC), BarEnd: time.Date(2026, 8, 28, 7, 0, 0, 0, time.UTC), Open: 9, High: 9.1, Low: 8.9, Close: 9.05, VolumeShares: 1000, AmountCNY: 9050, ProviderTimestamp: time.Date(2026, 8, 28, 7, 0, 0, 0, time.UTC), FetchedAt: now, RequestID: "request-1"}
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(pipelineProvider{id: "sina", rows: []marketdata.NormalizedKline{bar}}))
	require.NoError(t, registry.Register(pipelineProvider{id: "tencent", err: marketdata.ErrNoClosedBar}))
	router, err := marketdata.NewRouter(registry, 2, pipelineClock{now}, func(time.Duration) {})
	require.NoError(t, err)
	storage := &pipelineStorage{}
	pipeline := &KlinePipeline{Router: router, Storage: storage, CandidateChain: []string{"sina", "tencent"}, Now: func() time.Time { return now }}
	payload, err := pipeline.Execute(context.Background(), Request{BatchID: "batch-1", BatchKind: domain.BatchKindBackfill, SpaceID: StockCNSpaceID, DatasetID: StockCNDatasetID, Frequency: "1m", Provider: "sina", MarketType: "equity", RequestID: "request-1", Items: []domain.CollectionItem{{SubjectID: "600000.XSHG", Symbol: "sh600000", Provider: "sina", MarketType: "equity", DataType: "kline", DatasetID: StockCNDatasetID, Frequency: "1m", StartTime: "2026-08-28T06:59:00Z", BarLimit: 3}}})
	require.NoError(t, err)
	require.Equal(t, "succeeded", payload.GetStatus())
	require.Equal(t, 1, storage.writes)
	require.Equal(t, "batch-1", storage.sourceEventID)
	require.Len(t, storage.rows, 1)
	row := storage.rows[0]
	require.Equal(t, StockCNSpaceID, row.GetKey().GetSpaceId())
	require.Equal(t, StockCNDatasetID, row.GetKey().GetDatasetId())
	require.Empty(t, row.GetKey().GetTimeSeries().GetSeriesTag())
	fields := make(map[string]*storagepb.TypedValue, len(row.GetFields()))
	for _, field := range row.GetFields() {
		fields[field.GetFieldId()] = field.GetValue()
	}
	require.Len(t, fields, 18)
	require.Equal(t, "sina", fields["source_provider"].GetStringValue())
	require.Equal(t, int64(1), fields["route_rank"].GetIntValue())
	require.Equal(t, "shares", fields["volume_unit"].GetStringValue())
}

func TestKlinePipelineFiltersBarsBeforeConfiguredCoverageStart(t *testing.T) {
	now := time.Date(2026, 8, 29, 2, 0, 0, 0, time.UTC)
	before := marketdata.NormalizedKline{SubjectID: "600000.XSHG", ProviderID: "tencent", ProviderSymbol: "sh600000", Frequency: "1m", BarStart: time.Date(2026, 8, 28, 6, 58, 0, 0, time.UTC), BarEnd: time.Date(2026, 8, 28, 6, 59, 0, 0, time.UTC), Open: 9, High: 9.1, Low: 8.9, Close: 9.05, VolumeShares: 1000, AmountCNY: 0, ProviderTimestamp: time.Date(2026, 8, 28, 6, 59, 0, 0, time.UTC), FetchedAt: now, RequestID: "request-2"}
	after := before
	after.BarStart = time.Date(2026, 8, 28, 6, 59, 0, 0, time.UTC)
	after.BarEnd = time.Date(2026, 8, 28, 7, 0, 0, 0, time.UTC)
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(pipelineProvider{id: "sina", err: marketdata.ErrProtocol}))
	require.NoError(t, registry.Register(pipelineProvider{id: "tencent", rows: []marketdata.NormalizedKline{before, after}}))
	router, err := marketdata.NewRouter(registry, 2, pipelineClock{now}, func(time.Duration) {})
	require.NoError(t, err)
	storage := &pipelineStorage{}
	pipeline := &KlinePipeline{Router: router, Storage: storage, CandidateChain: []string{"sina", "tencent"}, Now: func() time.Time { return now }}
	payload, err := pipeline.Execute(context.Background(), Request{BatchID: "batch-2", BatchKind: domain.BatchKindBackfill, SpaceID: StockCNSpaceID, DatasetID: StockCNDatasetID, Frequency: "1m", Provider: "sina", MarketType: "equity", RequestID: "request-2", Items: []domain.CollectionItem{{SubjectID: "600000.XSHG", Symbol: "sh600000", Provider: "sina", MarketType: "equity", DataType: "kline", DatasetID: StockCNDatasetID, Frequency: "1m", StartTime: after.BarStart.Format(time.RFC3339), BarLimit: 3}}})
	require.NoError(t, err)
	require.Equal(t, "succeeded", payload.GetStatus())
	require.Len(t, storage.rows, 1)
	require.Equal(t, after.BarStart.Format(time.RFC3339Nano), storage.rows[0].GetKey().GetTimeSeries().GetDataTime())
	fields := make(map[string]*storagepb.TypedValue)
	for _, field := range storage.rows[0].GetFields() {
		fields[field.GetFieldId()] = field.GetValue()
	}
	require.Equal(t, "fallback", fields["quality_status"].GetStringValue())
	require.Equal(t, int64(2), fields["route_rank"].GetIntValue())
}
