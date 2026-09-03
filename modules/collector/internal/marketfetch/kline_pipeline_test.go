package marketfetch

import (
	"context"
	"fmt"
	"sync/atomic"
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
	id        string
	sourceID  string
	rows      []marketdata.NormalizedKline
	err       error
	delay     time.Duration
	rateLimit *marketdata.RateLimitPolicy
	calls     *int32
	request   *marketdata.KlineRequest
	rowsFor   func(marketdata.KlineRequest) []marketdata.NormalizedKline
}

func TestFetchKlinesFromChainRetriesProviderThreeTimesBeforeFallback(t *testing.T) {
	var firstCalls, fallbackCalls int32
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(pipelineProvider{id: "sina", err: marketdata.ErrProtocol, calls: &firstCalls}))
	bar := marketdata.NormalizedKline{
		SubjectID: "600000.XSHG", ProviderID: "tdx", ProviderSymbol: "sh600000", Frequency: "1m",
		BarStart: time.Date(2026, 9, 3, 3, 0, 0, 0, time.UTC), BarEnd: time.Date(2026, 9, 3, 3, 1, 0, 0, time.UTC),
		Open: 9, High: 9.1, Low: 8.9, Close: 9.05, VolumeShares: 1000, AmountCNY: 9050,
		ProviderTimestamp: time.Date(2026, 9, 3, 3, 1, 0, 0, time.UTC), FetchedAt: time.Now().UTC(), RequestID: "retry-3",
	}
	require.NoError(t, registry.Register(pipelineProvider{id: "tdx", rows: []marketdata.NormalizedKline{bar}, calls: &fallbackCalls}))
	router, err := marketdata.NewRouter(registry, 10, pipelineClock{now: time.Now().UTC()}, nil)
	require.NoError(t, err)

	rows, selected, _, err := fetchKlinesFromChain(context.Background(), router.NewSession(), marketdata.KlineRequest{
		MarketID: "stock_cn", ExchangeID: "XSHG", SubjectID: "600000.XSHG", ProviderSymbol: "sh600000",
		Frequency: "1m", Limit: 1, RequestID: "retry-3",
	}, []string{"sina", "tdx"}, 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "tdx", selected)
	require.Equal(t, int32(3), atomic.LoadInt32(&firstCalls))
	require.Equal(t, int32(1), atomic.LoadInt32(&fallbackCalls))
}

func TestStockCNShouldCollectMinuteHonorsSessionsAndClosedDays(t *testing.T) {
	calendar, err := stockmarket.LoadCalendar("../../config/markets/stock_cn/calendar.yaml")
	require.NoError(t, err)
	location := calendar.Location()
	openMinute := time.Date(2026, 8, 28, 9, 31, 0, 0, location)
	shouldCollect, err := stockCNShouldCollectMinute(calendar, openMinute, 0)
	require.NoError(t, err)
	require.True(t, shouldCollect)
	noon := time.Date(2026, 8, 28, 12, 0, 0, 0, location)
	shouldCollect, err = stockCNShouldCollectMinute(calendar, noon, 0)
	require.NoError(t, err)
	require.False(t, shouldCollect)
	weekend := time.Date(2026, 8, 29, 10, 0, 0, 0, location)
	shouldCollect, err = stockCNShouldCollectMinute(calendar, weekend, 0)
	require.NoError(t, err)
	require.False(t, shouldCollect)
}

func TestStockCNShouldCollectMinuteHonorsSettleDelay(t *testing.T) {
	calendar, err := stockmarket.LoadCalendar("../../config/markets/stock_cn/calendar.yaml")
	require.NoError(t, err)
	location := calendar.Location()
	barEnd := time.Date(2026, 8, 28, 9, 31, 0, 0, location)
	shouldCollect, err := stockCNShouldCollectMinute(calendar, barEnd.Add(4*time.Second), 5*time.Second)
	require.NoError(t, err)
	require.False(t, shouldCollect)
	shouldCollect, err = stockCNShouldCollectMinute(calendar, barEnd.Add(5*time.Second), 5*time.Second)
	require.NoError(t, err)
	require.True(t, shouldCollect)
}

func (p pipelineProvider) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{ID: p.id, SourceID: p.sourceID, DisplayName: p.id, Hosts: []string{p.id + ".test"}}
}

func (p pipelineProvider) KlineSpec() marketdata.KlineSpec {
	rateLimit := marketdata.RateLimitPolicy{RequestsPerSecond: 100, Burst: 3, MaxConcurrent: 1, Cooldown: time.Second, RequestTimeout: time.Second}
	if p.rateLimit != nil {
		rateLimit = *p.rateLimit
	}
	if p.id == "binance" {
		return marketdata.KlineSpec{Markets: []string{"crypto"}, Exchanges: []string{"binance"}, Frequencies: []string{"1m", "1h"}, CompleteOHLCV: true, HasAmount: true, MaxBarsPerRequest: 1000, TimestampMode: marketdata.TimestampModeOpen, RateLimit: rateLimit, History: marketdata.KlineHistoryCapability{SupportsArbitraryRange: true}}
	}
	return marketdata.KlineSpec{Markets: []string{"stock_cn"}, Exchanges: []string{"XSHG"}, Frequencies: []string{"1m"}, CompleteOHLCV: true, HasAmount: true, MaxBarsPerRequest: 3, TimestampMode: marketdata.TimestampModeOpen, RateLimit: rateLimit, History: marketdata.KlineHistoryCapability{MaxLookback: 7 * 24 * time.Hour}}
}

func (p pipelineProvider) FetchKlines(ctx context.Context, req marketdata.KlineRequest) ([]marketdata.NormalizedKline, error) {
	if p.request != nil {
		*p.request = req
	}
	if p.calls != nil {
		atomic.AddInt32(p.calls, 1)
	}
	if p.delay > 0 {
		select {
		case <-time.After(p.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if p.rowsFor != nil {
		return p.rowsFor(req), p.err
	}
	return p.rows, p.err
}

func TestKlinePipelineCanaryUsesLatestClosedCalendarSession(t *testing.T) {
	now := time.Date(2026, 8, 30, 4, 0, 0, 0, time.UTC) // Sunday, 12:00 Asia/Shanghai.
	barStart := time.Date(2026, 8, 28, 6, 59, 0, 0, time.UTC)
	bar := marketdata.NormalizedKline{SubjectID: "600000.XSHG", ProviderID: "sina", ProviderSymbol: "sh600000", Frequency: "1m", BarStart: barStart, BarEnd: barStart.Add(time.Minute), Open: 9, High: 9.1, Low: 8.9, Close: 9.05, VolumeShares: 1000, AmountCNY: 9050, ProviderTimestamp: barStart.Add(time.Minute), FetchedAt: now, RequestID: "canary"}
	var observed marketdata.KlineRequest
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(pipelineProvider{id: "sina", rows: []marketdata.NormalizedKline{bar}, request: &observed}))
	require.NoError(t, registry.Register(pipelineProvider{id: "tencent", err: marketdata.ErrNoClosedBar}))
	router, err := marketdata.NewRouter(registry, 2, pipelineClock{now}, nil)
	require.NoError(t, err)
	calendar, err := stockmarket.LoadCalendar("../../config/markets/stock_cn/calendar.yaml")
	require.NoError(t, err)
	storage := &pipelineStorage{}
	pipeline := &KlinePipeline{Router: router, Storage: storage, CandidateChain: []string{"sina", "tencent"}, Calendar: calendar, SettleDelay: 5 * time.Second, Now: func() time.Time { return now }}
	payload, err := pipeline.Execute(context.Background(), Request{BatchID: "canary", BatchKind: domain.BatchKindBackfill, SpaceID: StockCNSpaceID, DatasetID: StockCNDatasetID, Frequency: "1m", Provider: "sina", MarketType: "equity", RequestID: "canary", Items: []domain.CollectionItem{{SubjectID: bar.SubjectID, Symbol: bar.ProviderSymbol, Provider: "sina", MarketType: "equity", DataType: "kline", DatasetID: StockCNDatasetID, Frequency: "1m", StartTime: now.Add(-23 * time.Hour).Format(time.RFC3339Nano), BarLimit: 3, Canary: true}}})
	require.NoError(t, err)
	require.Equal(t, "succeeded", payload.GetStatus())
	require.Equal(t, barStart, observed.StartTime)
	require.Equal(t, bar.BarEnd, observed.EndTime)
	require.Equal(t, bar.BarEnd, observed.HistoryAsOf)
}

func TestKlinePipelineSharesInvocationBreakerAcrossThirtySubjectsAndKeepsFallbackWithinDeadline(t *testing.T) {
	now := time.Date(2026, 8, 29, 2, 0, 0, 0, time.UTC)
	bar := marketdata.NormalizedKline{SubjectID: "600000.XSHG", ProviderID: "tencent", ProviderSymbol: "sh600000", Frequency: "1m", BarStart: time.Date(2026, 8, 28, 6, 59, 0, 0, time.UTC), BarEnd: time.Date(2026, 8, 28, 7, 0, 0, 0, time.UTC), Open: 9, High: 9.1, Low: 8.9, Close: 9.05, VolumeShares: 1000, AmountCNY: 9050, ProviderTimestamp: time.Date(2026, 8, 28, 7, 0, 0, 0, time.UTC), FetchedAt: now, RequestID: "request-30"}
	rateLimit := &marketdata.RateLimitPolicy{RequestsPerSecond: 1000, Burst: 30, MaxConcurrent: 30, Cooldown: time.Second, RequestTimeout: 5 * time.Second}
	var firstCalls, fallbackCalls int32
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(pipelineProvider{id: "sina", err: marketdata.ErrProtocol, delay: 2 * time.Second, rateLimit: rateLimit, calls: &firstCalls}))
	require.NoError(t, registry.Register(pipelineProvider{id: "tencent", rateLimit: rateLimit, calls: &fallbackCalls, rowsFor: func(req marketdata.KlineRequest) []marketdata.NormalizedKline {
		row := bar
		row.SubjectID = req.SubjectID
		row.ProviderSymbol = req.ProviderSymbol
		row.RequestID = req.RequestID
		return []marketdata.NormalizedKline{row}
	}}))
	router, err := marketdata.NewRouter(registry, 2, pipelineClock{now}, nil)
	require.NoError(t, err)

	items := make([]domain.CollectionItem, 0, 30)
	for index := 0; index < 30; index++ {
		subjectID := fmt.Sprintf("%06d.XSHG", 600000+index)
		items = append(items, domain.CollectionItem{SubjectID: subjectID, Symbol: "sh" + subjectID[:6], Provider: "sina", MarketType: "equity", DataType: "kline", DatasetID: StockCNDatasetID, Frequency: "1m", BarLimit: 3})
	}
	pipeline := &KlinePipeline{Router: router, Storage: &pipelineStorage{}, CandidateChain: []string{"sina", "tencent"}, Now: func() time.Time { return now }}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	payload, err := pipeline.Execute(ctx, Request{BatchID: "batch-30", BatchKind: domain.BatchKindRealtime, SpaceID: StockCNSpaceID, DatasetID: StockCNDatasetID, Frequency: "1m", Provider: "sina", MarketType: "equity", RequestID: "request-30", Concurrency: 30, Items: items})
	require.NoError(t, err)
	require.Equal(t, "succeeded", payload.GetStatus())
	require.Len(t, pipeline.Storage.(*pipelineStorage).rows, 30)
	require.Equal(t, int32(2), atomic.LoadInt32(&firstCalls))
	require.Equal(t, int32(30), atomic.LoadInt32(&fallbackCalls))
}

func TestKlinePipelineRejectsRealtimeBarsInsideSettleWindow(t *testing.T) {
	now := time.Date(2026, 8, 28, 7, 0, 3, 0, time.UTC)
	barEnd := now.Truncate(time.Minute)
	bar := marketdata.NormalizedKline{
		SubjectID: "600000.XSHG", ProviderID: "sina", ProviderSymbol: "sh600000", Frequency: "1m",
		BarStart: barEnd.Add(-time.Minute), BarEnd: barEnd, Open: 9, High: 9.1, Low: 8.9, Close: 9.05,
		VolumeShares: 1000, AmountCNY: 9050, ProviderTimestamp: barEnd, FetchedAt: now, RequestID: "settle",
	}
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(pipelineProvider{id: "sina", rows: []marketdata.NormalizedKline{bar}}))
	require.NoError(t, registry.Register(pipelineProvider{id: "tencent", err: marketdata.ErrNoClosedBar}))
	router, err := marketdata.NewRouter(registry, 2, pipelineClock{now}, nil)
	require.NoError(t, err)
	storage := &pipelineStorage{}
	pipeline := &KlinePipeline{Router: router, Storage: storage, CandidateChain: []string{"sina", "tencent"}, SettleDelay: 5 * time.Second, Now: func() time.Time { return now }}
	payload, err := pipeline.Execute(context.Background(), Request{BatchID: "settle", BatchKind: domain.BatchKindRealtime, SpaceID: StockCNSpaceID, DatasetID: StockCNDatasetID, Frequency: "1m", Provider: "sina", MarketType: "equity", RequestID: "settle", Items: []domain.CollectionItem{{SubjectID: bar.SubjectID, Symbol: bar.ProviderSymbol, Provider: "sina", MarketType: "equity", DataType: "kline", DatasetID: StockCNDatasetID, Frequency: "1m", BarLimit: 1}}})
	require.NoError(t, err)
	require.Equal(t, "failed", payload.GetStatus())
	require.Empty(t, storage.rows)
}

func TestKlinePipelineDoesNotFallbackAcrossBoundSources(t *testing.T) {
	now := time.Date(2026, 8, 28, 7, 5, 0, 0, time.UTC)
	bar := marketdata.NormalizedKline{
		SubjectID: "600000.XSHG", ProviderID: "tencent", ProviderSymbol: "sh600000", Frequency: "1m",
		BarStart: now.Add(-6 * time.Minute), BarEnd: now.Add(-5 * time.Minute),
		Open: 9, High: 9.1, Low: 8.9, Close: 9.05, VolumeShares: 1000, AmountCNY: 9050,
		ProviderTimestamp: now.Add(-5 * time.Minute), FetchedAt: now, RequestID: "bound-source",
	}
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(pipelineProvider{id: "sina", sourceID: "sina_http", err: marketdata.ErrProtocol}))
	require.NoError(t, registry.Register(pipelineProvider{id: "tencent", sourceID: "tencent_http", rows: []marketdata.NormalizedKline{bar}}))
	router, err := marketdata.NewRouter(registry, 2, pipelineClock{now}, nil)
	require.NoError(t, err)
	storage := &pipelineStorage{}
	pipeline := &KlinePipeline{
		Router: router, Storage: storage, CandidateChain: []string{"sina"},
		SourceID: "sina_http", MarketID: StockCNSpaceID, SpaceID: StockCNSpaceID,
		DatasetID: StockCNDatasetID, Now: func() time.Time { return now },
	}
	payload, err := pipeline.Execute(context.Background(), Request{
		BatchID: "bound-source", BatchKind: domain.BatchKindRealtime,
		SpaceID: StockCNSpaceID, DatasetID: StockCNDatasetID, Frequency: "1m",
		Provider: "sina", SourceID: "sina_http", MarketType: "equity", RequestID: "bound-source",
		Items: []domain.CollectionItem{{
			SubjectID: "600000.XSHG", Symbol: "sh600000", Provider: "sina", SourceID: "sina_http",
			MarketType: "equity", DataType: "kline", DatasetID: StockCNDatasetID, Frequency: "1m", BarLimit: 1,
		}},
	})
	require.NoError(t, err)
	require.Equal(t, "failed", payload.GetStatus())
	require.Empty(t, storage.rows)
}

func TestKlinePipelinePersistsBoundProviderAndSourceIdentity(t *testing.T) {
	now := time.Date(2026, 8, 28, 7, 5, 0, 0, time.UTC)
	bar := marketdata.NormalizedKline{
		SubjectID: "600000.XSHG", ProviderID: "sina", SourceID: "stock_cn_minute_http",
		ProviderSymbol: "sh600000", Frequency: "1m",
		BarStart: now.Add(-6 * time.Minute), BarEnd: now.Add(-5 * time.Minute),
		Open: 9, High: 9.1, Low: 8.9, Close: 9.05, VolumeShares: 1000, AmountCNY: 9050,
		ProviderTimestamp: now.Add(-5 * time.Minute), FetchedAt: now, RequestID: "source-row",
	}
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(pipelineProvider{id: "sina", sourceID: "stock_cn_minute_http", rows: []marketdata.NormalizedKline{bar}}))
	router, err := marketdata.NewRouter(registry, 2, pipelineClock{now}, nil)
	require.NoError(t, err)
	storage := &pipelineStorage{}
	pipeline := &KlinePipeline{
		Router: router, Storage: storage, CandidateChain: []string{"sina"},
		SourceID: "stock_cn_minute_http", MarketID: StockCNSpaceID, SpaceID: StockCNSpaceID,
		DatasetID: StockCNDatasetID, Now: func() time.Time { return now },
	}
	_, err = pipeline.Execute(context.Background(), Request{
		BatchID: "source-row", BatchKind: domain.BatchKindRealtime,
		SpaceID: StockCNSpaceID, DatasetID: StockCNDatasetID, Frequency: "1m",
		Provider: "sina", SourceID: "stock_cn_minute_http", MarketType: "equity", RequestID: "source-row",
		Items: []domain.CollectionItem{{
			SubjectID: bar.SubjectID, Symbol: bar.ProviderSymbol, Provider: "sina", SourceID: "stock_cn_minute_http",
			MarketType: "equity", DataType: "kline", DatasetID: StockCNDatasetID, Frequency: "1m", BarLimit: 1,
		}},
	})
	require.NoError(t, err)
	require.Len(t, storage.rows, 1)
	fields := make(map[string]*storagepb.TypedValue)
	for _, field := range storage.rows[0].GetFields() {
		fields[field.GetFieldId()] = field.GetValue()
	}
	require.Equal(t, "sina", fields["provider_id"].GetStringValue())
	require.Equal(t, "stock_cn_minute_http", fields["source_id"].GetStringValue())
}

func TestKlinePipelineRejectsRollingHistoryWithoutProvenCoverage(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	recent := marketdata.NormalizedKline{SubjectID: "600000.XSHG", ProviderID: "sina", ProviderSymbol: "sh600000", Frequency: "1m", BarStart: now.Add(-time.Minute), BarEnd: now, Open: 1, High: 1, Low: 1, Close: 1, VolumeShares: 1, AmountCNY: 1, ProviderTimestamp: now, FetchedAt: now, RequestID: "history"}
	registry := marketdata.NewRegistry()
	for _, id := range []string{"sina", "tencent"} {
		require.NoError(t, registry.Register(pipelineProvider{id: id, rows: []marketdata.NormalizedKline{recent}}))
	}
	router, err := marketdata.NewRouter(registry, 3, pipelineClock{now}, nil)
	require.NoError(t, err)
	pipeline := &KlinePipeline{Router: router, Storage: &pipelineStorage{}, CandidateChain: []string{"sina", "tencent"}, Now: func() time.Time { return now }}
	payload, err := pipeline.Execute(context.Background(), Request{BatchID: "history", BatchKind: domain.BatchKindGapRepair, SpaceID: StockCNSpaceID, DatasetID: StockCNDatasetID, Frequency: "1m", Provider: "sina", MarketType: "equity", RequestID: "history", Items: []domain.CollectionItem{{SubjectID: recent.SubjectID, Symbol: recent.ProviderSymbol, Provider: "sina", MarketType: "equity", DataType: "kline", DatasetID: StockCNDatasetID, Frequency: "1m", StartTime: now.Add(-2 * time.Hour).Format(time.RFC3339Nano), BarLimit: 3}}})
	require.NoError(t, err)
	require.Equal(t, "failed", payload.GetStatus())
	require.Contains(t, payload.GetErrorSummary(), "coverage")
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
	require.Len(t, fields, 19)
	require.Equal(t, "sina", fields["source_provider"].GetStringValue())
	require.Equal(t, int64(1), fields["route_rank"].GetIntValue())
	require.Equal(t, "shares", fields["volume_unit"].GetStringValue())
	require.Equal(t, "reported", fields["amount_quality"].GetStringValue())
}

func TestKlinePipelineUsesRetrySourceEventID(t *testing.T) {
	now := time.Date(2026, 8, 29, 2, 0, 0, 0, time.UTC)
	bar := marketdata.NormalizedKline{SubjectID: "600000.XSHG", ProviderID: "sina", ProviderSymbol: "sh600000", Frequency: "1m", BarStart: time.Date(2026, 8, 28, 6, 59, 0, 0, time.UTC), BarEnd: time.Date(2026, 8, 28, 7, 0, 0, 0, time.UTC), Open: 9, High: 9.1, Low: 8.9, Close: 9.05, VolumeShares: 1000, AmountCNY: 9050, ProviderTimestamp: time.Date(2026, 8, 28, 7, 0, 0, 0, time.UTC), FetchedAt: now, RequestID: "request-retry"}
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(pipelineProvider{id: "sina", rows: []marketdata.NormalizedKline{bar}}))
	require.NoError(t, registry.Register(pipelineProvider{id: "tencent", err: marketdata.ErrNoClosedBar}))
	router, err := marketdata.NewRouter(registry, 2, pipelineClock{now}, func(time.Duration) {})
	require.NoError(t, err)
	storage := &pipelineStorage{}
	pipeline := &KlinePipeline{Router: router, Storage: storage, CandidateChain: []string{"sina", "tencent"}, Now: func() time.Time { return now }}
	_, err = pipeline.Execute(context.Background(), Request{BatchID: "retry-attempt-batch", BatchKind: domain.BatchKindGapRepair, SpaceID: StockCNSpaceID, DatasetID: StockCNDatasetID, Frequency: "1m", Provider: "sina", MarketType: "equity", RequestID: "request-retry", Items: []domain.CollectionItem{{SubjectID: bar.SubjectID, Symbol: bar.ProviderSymbol, SourceEventID: "retry-key", MarketType: "equity", DataType: "kline", DatasetID: StockCNDatasetID, Frequency: "1m", StartTime: bar.BarStart.Format(time.RFC3339), BarLimit: 3}}})
	require.NoError(t, err)
	require.Equal(t, "retry-key", storage.sourceEventID)
}

func TestKlinePipelineAcceptsCryptoHourFrequency(t *testing.T) {
	now := time.Date(2026, 8, 29, 2, 0, 0, 0, time.UTC)
	barStart := time.Date(2026, 8, 29, 1, 0, 0, 0, time.UTC)
	bar := marketdata.NormalizedKline{SubjectID: "BTC-USDT-SPOT", ProviderID: "binance", ProviderSymbol: "BTCUSDT", Frequency: "1h", BarStart: barStart, BarEnd: barStart.Add(time.Hour), Open: 100, High: 110, Low: 90, Close: 105, VolumeShares: 12, AmountCNY: 1234, ProviderTimestamp: barStart.Add(time.Hour), FetchedAt: now, RequestID: "request-hour"}
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(pipelineProvider{id: "binance", rows: []marketdata.NormalizedKline{bar}}))
	router, err := marketdata.NewRouter(registry, 1, pipelineClock{now}, func(time.Duration) {})
	require.NoError(t, err)
	storage := &pipelineStorage{}
	pipeline := &KlinePipeline{Router: router, Storage: storage, CandidateChain: []string{"binance"}, SpaceID: "crypto_market", MarketID: "crypto", ProductType: marketdata.ProductSpot, InstrumentType: marketdata.InstrumentSpot, DatasetID: "binance_spot_kline_1h", Now: func() time.Time { return now }}
	payload, err := pipeline.Execute(context.Background(), Request{BatchID: "batch-hour", BatchKind: domain.BatchKindBackfill, SpaceID: "crypto_market", DatasetID: "binance_spot_kline_1h", Frequency: "1h", Provider: "binance", MarketType: "spot", RequestID: "request-hour", Items: []domain.CollectionItem{{SubjectID: bar.SubjectID, Symbol: bar.ProviderSymbol, Provider: "binance", MarketType: "spot", DataType: "kline", DatasetID: "binance_spot_kline_1h", Frequency: "1h", StartTime: barStart.Format(time.RFC3339), BarLimit: 1}}})
	require.NoError(t, err)
	require.Equal(t, "succeeded", payload.GetStatus())
	require.Len(t, storage.rows, 1)
	require.Equal(t, "1h", storage.rows[0].GetKey().GetTimeSeries().GetFreq())
}

func TestRequestKlineRouteIDUsesCryptoFrequency(t *testing.T) {
	for _, test := range []struct {
		product   marketdata.ProductType
		frequency string
		want      string
	}{
		{product: marketdata.ProductSpot, frequency: "1h", want: "binance_spot_kline_1h"},
		{product: marketdata.ProductSwap, frequency: "1w", want: "binance_swap_kline_1w"},
	} {
		pipeline := &KlinePipeline{SpaceID: "crypto_market", MarketID: "crypto", ProductType: test.product}
		require.Equal(t, test.want, requestKlineRouteID(pipeline, test.frequency))
	}
}

func TestKlinePipelineFiltersBarsBeforeConfiguredCoverageStart(t *testing.T) {
	now := time.Date(2026, 8, 29, 2, 0, 0, 0, time.UTC)
	before := marketdata.NormalizedKline{SubjectID: "600000.XSHG", ProviderID: "tencent", ProviderSymbol: "sh600000", Frequency: "1m", BarStart: time.Date(2026, 8, 28, 6, 58, 0, 0, time.UTC), BarEnd: time.Date(2026, 8, 28, 6, 59, 0, 0, time.UTC), Open: 9, High: 9.1, Low: 8.9, Close: 9.05, VolumeShares: 1000, AmountCNY: 0, ProviderTimestamp: time.Date(2026, 8, 28, 6, 59, 0, 0, time.UTC), FetchedAt: now, RequestID: "request-2"}
	after := before
	after.BarStart = time.Date(2026, 8, 28, 6, 59, 0, 0, time.UTC)
	after.BarEnd = time.Date(2026, 8, 28, 7, 0, 0, 0, time.UTC)
	outside := after
	outside.BarStart = after.BarEnd
	outside.BarEnd = outside.BarStart.Add(time.Minute)
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(pipelineProvider{id: "sina", err: marketdata.ErrProtocol}))
	require.NoError(t, registry.Register(pipelineProvider{id: "tencent", rows: []marketdata.NormalizedKline{before, after, outside}}))
	router, err := marketdata.NewRouter(registry, 2, pipelineClock{now}, func(time.Duration) {})
	require.NoError(t, err)
	storage := &pipelineStorage{}
	pipeline := &KlinePipeline{Router: router, Storage: storage, CandidateChain: []string{"sina", "tencent"}, Now: func() time.Time { return now }}
	payload, err := pipeline.Execute(context.Background(), Request{BatchID: "batch-2", BatchKind: domain.BatchKindBackfill, SpaceID: StockCNSpaceID, DatasetID: StockCNDatasetID, Frequency: "1m", Provider: "sina", MarketType: "equity", RequestID: "request-2", Items: []domain.CollectionItem{{SubjectID: "600000.XSHG", Symbol: "sh600000", Provider: "sina", MarketType: "equity", DataType: "kline", DatasetID: StockCNDatasetID, Frequency: "1m", StartTime: after.BarStart.Format(time.RFC3339), EndTime: after.BarEnd.Format(time.RFC3339), BarLimit: 3}}})
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

func TestNormalizeCandidateChainKeepsConfiguredThreeProviderRoute(t *testing.T) {
	require.Equal(t, []string{"sina", "tencent", "eastmoney"}, normalizeCandidateChain([]string{"sina", "tencent", "eastmoney"}, "sina"))
	require.Equal(t, []string{"eastmoney", "sina", "tencent"}, normalizeCandidateChain([]string{"sina", "tencent"}, "eastmoney"))
}

func TestKlinePipelineUsesThirdProviderAfterThreeAttempts(t *testing.T) {
	now := time.Date(2026, 8, 29, 2, 0, 0, 0, time.UTC)
	bar := marketdata.NormalizedKline{SubjectID: "600000.XSHG", ProviderID: "eastmoney", ProviderSymbol: "sh600000", Frequency: "1m", BarStart: time.Date(2026, 8, 28, 6, 59, 0, 0, time.UTC), BarEnd: time.Date(2026, 8, 28, 7, 0, 0, 0, time.UTC), Open: 9, High: 9.1, Low: 8.9, Close: 9.05, VolumeShares: 1000, AmountCNY: 9050, ProviderTimestamp: time.Date(2026, 8, 28, 7, 0, 0, 0, time.UTC), FetchedAt: now, RequestID: "request-third"}
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(pipelineProvider{id: "sina", err: marketdata.ErrTimeout}))
	require.NoError(t, registry.Register(pipelineProvider{id: "tencent", err: marketdata.ErrProtocol}))
	require.NoError(t, registry.Register(pipelineProvider{id: "eastmoney", rows: []marketdata.NormalizedKline{bar}}))
	router, err := marketdata.NewRouter(registry, 3, pipelineClock{now}, func(time.Duration) {})
	require.NoError(t, err)
	storage := &pipelineStorage{}
	pipeline := &KlinePipeline{Router: router, Storage: storage, CandidateChain: []string{"sina", "tencent", "eastmoney"}, Now: func() time.Time { return now }}
	payload, err := pipeline.Execute(context.Background(), Request{BatchID: "batch-third", BatchKind: domain.BatchKindBackfill, SpaceID: StockCNSpaceID, DatasetID: StockCNDatasetID, Frequency: "1m", Provider: "sina", MarketType: "equity", RequestID: "request-third", Items: []domain.CollectionItem{{SubjectID: "600000.XSHG", Symbol: "sh600000", Provider: "sina", MarketType: "equity", DataType: "kline", DatasetID: StockCNDatasetID, Frequency: "1m", StartTime: bar.BarStart.Format(time.RFC3339), BarLimit: 3}}})
	require.NoError(t, err)
	require.Equal(t, "succeeded", payload.GetStatus())
	require.Len(t, storage.rows, 1)
	fields := make(map[string]*storagepb.TypedValue)
	for _, field := range storage.rows[0].GetFields() {
		fields[field.GetFieldId()] = field.GetValue()
	}
	require.Equal(t, "fallback", fields["quality_status"].GetStringValue())
	require.Equal(t, int64(3), fields["route_rank"].GetIntValue())
}

func TestKlinePipelineAdvancesCandidateIndexAfterExhaustedChain(t *testing.T) {
	now := time.Date(2026, 8, 29, 2, 0, 0, 0, time.UTC)
	registry := marketdata.NewRegistry()
	for _, id := range []string{"sina", "tencent", "eastmoney"} {
		require.NoError(t, registry.Register(pipelineProvider{id: id, err: marketdata.ErrProtocol}))
	}
	router, err := marketdata.NewRouter(registry, 3, pipelineClock{now}, func(time.Duration) {})
	require.NoError(t, err)
	_, selected, next, err := fetchKlinesFromChain(context.Background(), router.NewSession(), marketdata.KlineRequest{
		MarketID: "stock_cn", ExchangeID: "XSHG", ProductType: marketdata.ProductEquity, InstrumentType: marketdata.InstrumentEquity,
		SubjectID: "600000.XSHG", ProviderSymbol: "sh600000", Frequency: "1m", Limit: 1, RequestID: "candidate-index",
	}, []string{"sina", "tencent", "eastmoney"}, 1)
	require.Error(t, err)
	require.Equal(t, "sina", selected)
	require.Equal(t, 1, next, "the next retry must start after all three providers receive three attempts")
}

func TestFetchKlinesFromChainFallsBackWhenRequestedIntervalIsEmpty(t *testing.T) {
	now := time.Date(2026, 8, 29, 2, 0, 0, 0, time.UTC)
	start := time.Date(2026, 8, 28, 6, 59, 0, 0, time.UTC)
	end := start.Add(time.Minute)
	valid := marketdata.NormalizedKline{SubjectID: "600000.XSHG", ProviderID: "tencent", ProviderSymbol: "sh600000", Frequency: "1m", BarStart: start, BarEnd: end, Open: 9, High: 9.1, Low: 8.9, Close: 9.05, VolumeShares: 1000, AmountCNY: 9050, ProviderTimestamp: end, FetchedAt: now, RequestID: "coverage-fallback"}
	outOfRange := valid
	outOfRange.ProviderID = "sina"
	outOfRange.BarStart = start.Add(-time.Minute)
	outOfRange.BarEnd = start
	outOfRange.ProviderTimestamp = start
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(pipelineProvider{id: "sina", rows: []marketdata.NormalizedKline{outOfRange}}))
	require.NoError(t, registry.Register(pipelineProvider{id: "tencent", rows: []marketdata.NormalizedKline{valid}}))
	router, err := marketdata.NewRouter(registry, 2, pipelineClock{now}, nil)
	require.NoError(t, err)

	rows, selected, _, err := fetchKlinesFromChain(context.Background(), router.NewSession(), marketdata.KlineRequest{
		MarketID: "stock_cn", ExchangeID: "XSHG", ProductType: marketdata.ProductEquity, InstrumentType: marketdata.InstrumentEquity,
		SubjectID: valid.SubjectID, ProviderSymbol: valid.ProviderSymbol, Frequency: "1m", Limit: 1,
		StartTime: start, EndTime: end, Now: now, RequestID: "coverage-fallback",
	}, []string{"sina", "tencent"}, 0)
	require.NoError(t, err)
	require.Equal(t, "tencent", selected)
	require.Equal(t, []marketdata.NormalizedKline{valid}, rows)
}
