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
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
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
	errFor    func() error
}

func TestKlinePipelineStockCNRowBindsToEnsuredDefaultSeries(t *testing.T) {
	period := time.Date(2026, 9, 30, 3, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		instrument marketdata.InstrumentType
		subject    string
		dataset    string
	}{
		{marketdata.InstrumentEquity, "600000.XSHG", StockCNDatasetID},
		{marketdata.InstrumentIndex, "000001.XSHG", "dataset_stockcn_index_kline_1d"},
		{marketdata.InstrumentConvertibleBond, "113001.XSHG", "dataset_stockcn_bond_kline_1m"},
	} {
		t.Run(string(test.instrument), func(t *testing.T) {
			item := domain.CollectionItem{SubjectID: test.subject, Symbol: "sh" + test.subject[:6], DatasetID: test.dataset, Provider: "sina", SourceID: "stockcn_http", MarketType: string(test.instrument), TargetDataTime: period.Format(time.RFC3339Nano), SeriesHash: "one-logical-series", ExpectedCount: 1, PeriodReservationID: "release-canary-123"}
			storage := &recordingPeriodFailureStorage{}
			scheduler := &Scheduler{Storage: func(string, string) (Storage, error) { return storage, nil }}
			require.NoError(t, scheduler.ensureDatasetPeriod(context.Background(), domain.CollectionTask{SpaceID: StockCNSpaceID}, []domain.CollectionItem{item}, "1m", period, period.Add(time.Minute)))
			require.Len(t, storage.ensured, 1)
			ensured := storage.ensured[0]
			require.Equal(t, uint32(1), ensured.GetExpectedCount())
			require.Equal(t, item.PeriodReservationID, ensured.GetReservationId())
			// Match the composition root: no explicit SeriesTag for index or bond.
			pipeline := &KlinePipeline{SpaceID: StockCNSpaceID, MarketID: StockCNSpaceID, InstrumentType: test.instrument, DatasetID: test.dataset, SourceID: item.SourceID}
			req := Request{SpaceID: StockCNSpaceID, DatasetID: test.dataset, Frequency: "1m", SourceID: item.SourceID, MarketType: string(test.instrument), Items: []domain.CollectionItem{item}, RequirePeriodCommit: true}
			for rank, provider := range []string{"sina", "tencent", "tdx", "eastmoney"} {
				bar := marketdata.NormalizedKline{SubjectID: test.subject, ProviderID: provider, SourceID: provider + "_http", ProviderSymbol: item.Symbol, Frequency: "1m", BarStart: period, BarEnd: period.Add(time.Minute), Open: 9, High: 9.1, Low: 8.9, Close: 9.05, VolumeShares: 1000, AmountCNY: 9050, ProviderTimestamp: period.Add(time.Minute), FetchedAt: period.Add(2 * time.Minute), RequestID: "row-period-binding"}
				row, err := pipeline.rowFor(bar, req, "stockcn-route", rank+1)
				require.NoError(t, err)
				expectation, rows, enabled, err := periodCommitForDataset(req, test.dataset, []*storagepb.RowFieldUpsert{row})
				require.NoError(t, err, "actual rowFor output must bind to the Ensure index even after a provider fallback")
				require.True(t, enabled)
				require.Equal(t, "default", row.GetKey().GetTimeSeries().GetSeriesTag())
				require.Equal(t, ensured.GetSeriesSnapshot()[0].GetSeriesTag(), row.GetKey().GetTimeSeries().GetSeriesTag())
				require.Equal(t, ensured.GetSeriesHash(), expectation.GetSeriesHash())
				require.Equal(t, item.PeriodReservationID, expectation.GetReservationId())
				require.Equal(t, uint32(1), expectation.GetExpectedCount())
				require.Len(t, rows, 1)
				require.Zero(t, rows[0].GetSeriesIndex())
			}
		})
	}
}

func TestPeriodCommitDisabledIgnoresResidualBindings(t *testing.T) {
	req := Request{Items: []domain.CollectionItem{{DatasetID: "bars", SubjectID: "BTC-USDT", SeriesHash: "old", ExpectedCount: 1}}}
	_, _, enabled, err := periodCommitForDataset(req, "bars", []*storagepb.RowFieldUpsert{{}})
	require.NoError(t, err)
	require.False(t, enabled)
}

func TestKlinePipelineNonStockCNRowPreservesProviderSourceTag(t *testing.T) {
	period := time.Date(2026, 9, 30, 3, 0, 0, 0, time.UTC)
	bar := marketdata.NormalizedKline{SubjectID: "BTC-USDT", ProviderID: "binance", SourceID: "spot_http", ProviderSymbol: "BTCUSDT", Frequency: "1m", BarStart: period, BarEnd: period.Add(time.Minute), Open: 9, High: 9.1, Low: 8.9, Close: 9.05, VolumeShares: 1000, AmountCNY: 9050, ProviderTimestamp: period.Add(time.Minute), FetchedAt: period.Add(2 * time.Minute), RequestID: "non-stock-period-tag"}
	req := Request{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", SourceID: bar.SourceID, MarketType: "spot"}
	pipeline := &KlinePipeline{MarketID: "crypto", InstrumentType: marketdata.InstrumentSpot, SourceID: bar.SourceID}
	row, err := pipeline.rowFor(bar, req, "binance-spot", 1)
	require.NoError(t, err)
	require.Equal(t, defaultMarketSeriesTag(bar.ProviderID, bar.SourceID, req.MarketType), row.GetKey().GetTimeSeries().GetSeriesTag())
	bar.ProviderID = "okx"
	bar.SourceID = "okx_spot_http"
	fallback, err := pipeline.rowFor(bar, req, "okx-spot", 2)
	require.NoError(t, err)
	require.Equal(t, defaultMarketSeriesTag(bar.ProviderID, bar.SourceID, req.MarketType), fallback.GetKey().GetTimeSeries().GetSeriesTag())
	require.NotEqual(t, row.GetKey().GetTimeSeries().GetSeriesTag(), fallback.GetKey().GetTimeSeries().GetSeriesTag())
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
		MarketID: "stockcn", ExchangeID: "XSHG", SubjectID: "600000.XSHG", ProviderSymbol: "sh600000",
		Frequency: "1m", Limit: 1, RequestID: "retry-3",
	}, []string{"sina", "tdx"}, 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "tdx", selected)
	require.Equal(t, int32(3), atomic.LoadInt32(&firstCalls))
	require.Equal(t, int32(1), atomic.LoadInt32(&fallbackCalls))
}

func TestFetchKlinesFromChainUsesConfiguredHTTPMaxAttempts(t *testing.T) {
	t.Setenv("MOOX_FETCH_HTTP_MAX_ATTEMPTS", "4")
	var calls int32
	bar := marketdata.NormalizedKline{
		SubjectID: "BTC-USDT", ProviderID: "binance", ProviderSymbol: "BTCUSDT", Frequency: "1m",
		BarStart: time.Date(2026, 9, 3, 3, 0, 0, 0, time.UTC), BarEnd: time.Date(2026, 9, 3, 3, 1, 0, 0, time.UTC),
		Open: 100, High: 101, Low: 99, Close: 100.5, VolumeShares: 10, AmountCNY: 1005,
		ProviderTimestamp: time.Date(2026, 9, 3, 3, 1, 0, 0, time.UTC), FetchedAt: time.Now().UTC(), RequestID: "retry-4",
	}
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(pipelineProvider{
		id: "binance", rows: []marketdata.NormalizedKline{bar}, calls: &calls,
		errFor: func() error {
			if atomic.LoadInt32(&calls) < 4 {
				return marketdata.ErrTimeout
			}
			return nil
		},
	}))
	router, err := marketdata.NewRouter(registry, 10, nil, nil)
	require.NoError(t, err)

	rows, selected, _, err := fetchKlinesFromChain(context.Background(), router.NewSession(), marketdata.KlineRequest{
		MarketID: "crypto", ExchangeID: "binance", SubjectID: "BTC-USDT", ProviderSymbol: "BTCUSDT",
		Frequency: "1m", Limit: 1, RequestID: "retry-4",
	}, []string{"binance"}, 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "binance", selected)
	require.Equal(t, int32(4), atomic.LoadInt32(&calls))
}

func TestFetchKlinesFromChainUsesConfiguredRequestTimeout(t *testing.T) {
	t.Setenv("MOOX_FETCH_HTTP_MAX_ATTEMPTS", "1")
	t.Setenv("MOOX_FETCH_REQUEST_TIMEOUT_MS", "10")
	var calls int32
	bar := marketdata.NormalizedKline{
		SubjectID: "BTC-USDT", ProviderID: "binance", ProviderSymbol: "BTCUSDT", Frequency: "1m",
		BarStart: time.Date(2026, 9, 3, 3, 0, 0, 0, time.UTC), BarEnd: time.Date(2026, 9, 3, 3, 1, 0, 0, time.UTC),
		Open: 100, High: 101, Low: 99, Close: 100.5, VolumeShares: 10, AmountCNY: 1005,
		ProviderTimestamp: time.Date(2026, 9, 3, 3, 1, 0, 0, time.UTC), FetchedAt: time.Now().UTC(), RequestID: "request-timeout",
	}
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(pipelineProvider{id: "binance", rows: []marketdata.NormalizedKline{bar}, calls: &calls, delay: 100 * time.Millisecond}))
	router, err := marketdata.NewRouter(registry, 10, pipelineClock{now: time.Now().UTC()}, nil)
	require.NoError(t, err)

	_, _, _, err = fetchKlinesFromChain(context.Background(), router.NewSession(), marketdata.KlineRequest{
		MarketID: "crypto", ExchangeID: "binance", SubjectID: "BTC-USDT", ProviderSymbol: "BTCUSDT",
		Frequency: "1m", Limit: 1, RequestID: "request-timeout",
	}, []string{"binance"}, 0)
	require.ErrorIs(t, err, marketdata.ErrTimeout)
	require.Equal(t, int32(1), atomic.LoadInt32(&calls))
}

func TestStockCNShouldCollectMinuteHonorsSessionsAndClosedDays(t *testing.T) {
	calendar, err := stockmarket.LoadCalendar("../../config/markets/stockcn/calendar.yaml")
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
	calendar, err := stockmarket.LoadCalendar("../../config/markets/stockcn/calendar.yaml")
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
	return marketdata.KlineSpec{Markets: []string{"stockcn"}, Exchanges: []string{"XSHG"}, Frequencies: []string{"1m"}, CompleteOHLCV: true, HasAmount: true, MaxBarsPerRequest: 3, TimestampMode: marketdata.TimestampModeOpen, RateLimit: rateLimit, History: marketdata.KlineHistoryCapability{MaxLookback: 7 * 24 * time.Hour}}
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
		err := p.err
		if p.errFor != nil {
			err = p.errFor()
		}
		return p.rowsFor(req), err
	}
	err := p.err
	if p.errFor != nil {
		err = p.errFor()
	}
	return p.rows, err
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
	calendar, err := stockmarket.LoadCalendar("../../config/markets/stockcn/calendar.yaml")
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
		SubjectID: "600000.XSHG", ProviderID: "sina", SourceID: "stockcn_minute_http",
		ProviderSymbol: "sh600000", Frequency: "1m",
		BarStart: now.Add(-6 * time.Minute), BarEnd: now.Add(-5 * time.Minute),
		Open: 9, High: 9.1, Low: 8.9, Close: 9.05, VolumeShares: 1000, AmountCNY: 9050,
		ProviderTimestamp: now.Add(-5 * time.Minute), FetchedAt: now, RequestID: "source-row",
	}
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(pipelineProvider{id: "sina", sourceID: "stockcn_minute_http", rows: []marketdata.NormalizedKline{bar}}))
	router, err := marketdata.NewRouter(registry, 2, pipelineClock{now}, nil)
	require.NoError(t, err)
	storage := &pipelineStorage{}
	pipeline := &KlinePipeline{
		Router: router, Storage: storage, CandidateChain: []string{"sina"},
		SourceID: "stockcn_minute_http", MarketID: StockCNSpaceID, SpaceID: StockCNSpaceID,
		DatasetID: StockCNDatasetID, Now: func() time.Time { return now },
	}
	_, err = pipeline.Execute(context.Background(), Request{
		BatchID: "source-row", BatchKind: domain.BatchKindRealtime,
		SpaceID: StockCNSpaceID, DatasetID: StockCNDatasetID, Frequency: "1m",
		Provider: "sina", SourceID: "stockcn_minute_http", MarketType: "equity", RequestID: "source-row",
		Items: []domain.CollectionItem{{
			SubjectID: bar.SubjectID, Symbol: bar.ProviderSymbol, Provider: "sina", SourceID: "stockcn_minute_http",
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
	require.Equal(t, "stockcn_minute_http", fields["source_id"].GetStringValue())
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

func TestKlinePipelineFetchesFrozenHistoricalTargetOutsideCurrentSession(t *testing.T) {
	now := time.Date(2026, 8, 28, 8, 30, 0, 0, time.UTC) // Friday 16:30 Asia/Shanghai.
	period := time.Date(2026, 8, 28, 6, 59, 0, 0, time.UTC)
	bar := marketdata.NormalizedKline{SubjectID: "600000.XSHG", ProviderID: "sina", ProviderSymbol: "sh600000", Frequency: "1m", BarStart: period, BarEnd: period.Add(time.Minute), Open: 1, High: 1, Low: 1, Close: 1, VolumeShares: 1, AmountCNY: 1, ProviderTimestamp: period.Add(time.Minute), FetchedAt: now, RequestID: "historical-target"}
	var observed marketdata.KlineRequest
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(pipelineProvider{id: "sina", rows: []marketdata.NormalizedKline{bar}, request: &observed}))
	router, err := marketdata.NewRouter(registry, 2, nil, nil)
	require.NoError(t, err)
	calendar, err := stockmarket.LoadCalendar("../../config/markets/stockcn/calendar.yaml")
	require.NoError(t, err)
	storage := &recordingTimerPeriodCommitStorage{}
	pipeline := &KlinePipeline{Router: router, Storage: storage, CandidateChain: []string{"sina"}, Calendar: calendar, SettleDelay: 5 * time.Second, Now: func() time.Time { return now }}
	item := domain.CollectionItem{InstanceID: "instance-1", SubjectID: bar.SubjectID, Symbol: bar.ProviderSymbol, Provider: "sina", MarketType: "equity", DataType: "kline", DatasetID: StockCNDatasetID, Frequency: "1m", TargetDataTime: period.Format(time.RFC3339Nano), SeriesIndex: 0, SeriesHash: "series-hash", ExpectedCount: 1, RequirePeriodCommit: true}
	target := domain.WriteTarget{ID: "target-1", SpaceID: StockCNSpaceID, InstanceID: item.InstanceID, TaskID: "task-1", DatasetID: StockCNDatasetID, SeriesIndex: 0, SeriesHash: item.SeriesHash, ExpectedCount: item.ExpectedCount, Frequency: "1m", TargetDataTime: item.TargetDataTime}
	payload, err := pipeline.Execute(context.Background(), Request{
		BatchID: "historical-target", BatchKind: domain.BatchKindRealtime, SpaceID: StockCNSpaceID, DatasetID: StockCNDatasetID,
		Frequency: "1m", Provider: "sina", SourceID: "stockcn_minute_http", MarketType: "equity", RequirePeriodCommit: true,
		Items: []domain.CollectionItem{item}, Targets: []domain.WriteTarget{target},
	})
	require.NoError(t, err)
	require.Equal(t, "succeeded", payload.GetStatus())
	require.Equal(t, period, observed.StartTime)
	require.Equal(t, period.Add(time.Minute), observed.EndTime)
	require.Equal(t, 1, storage.commits)
	require.Equal(t, []uint32{0}, storage.indexes)

	target.OutputFields = `["unsupported"]`
	noRowsStorage := &recordingTimerPeriodCommitStorage{}
	pipeline.Storage = noRowsStorage
	payload, err = pipeline.Execute(context.Background(), Request{
		BatchID: "unsupported-fields", BatchKind: domain.BatchKindRealtime, SpaceID: StockCNSpaceID, DatasetID: StockCNDatasetID,
		Frequency: "1m", Provider: "sina", SourceID: "stockcn_minute_http", MarketType: "equity", RequirePeriodCommit: true,
		Items: []domain.CollectionItem{item}, Targets: []domain.WriteTarget{target},
	})
	require.NoError(t, err)
	require.Equal(t, "failed", payload.GetStatus(), "a required target without any supported fields cannot succeed")
	require.Zero(t, noRowsStorage.commits)
	target.OutputFields = ""
	secondTarget := target
	secondTarget.ID, secondTarget.DatasetID = "target-2", "second-bars"
	target.OutputFields = `["unsupported"]`
	mixedFieldsStorage := &recordingTimerPeriodCommitStorage{}
	pipeline.Storage = mixedFieldsStorage
	payload, err = pipeline.Execute(context.Background(), Request{
		BatchID: "mixed-output-targets", BatchKind: domain.BatchKindRealtime, SpaceID: StockCNSpaceID, DatasetID: StockCNDatasetID,
		Frequency: "1m", Provider: "sina", SourceID: "stockcn_minute_http", MarketType: "equity", RequirePeriodCommit: true,
		Items: []domain.CollectionItem{item}, Targets: []domain.WriteTarget{target, secondTarget},
	})
	require.NoError(t, err)
	require.Equal(t, "failed", payload.GetStatus())
	require.Equal(t, 1, mixedFieldsStorage.commits, "unsupported output must not prevent an independent valid target from committing")
	require.Equal(t, "failed", payload.GetItems()[0].GetTargets()[0].GetStatus())
	require.Equal(t, "succeeded", payload.GetItems()[0].GetTargets()[1].GetStatus())
	target.OutputFields = ""
	partialStorage := &recordingTimerPeriodCommitStorage{failDataset: target.DatasetID}
	pipeline.Storage = partialStorage
	payload, err = pipeline.Execute(context.Background(), Request{
		BatchID: "two-targets", BatchKind: domain.BatchKindRealtime, SpaceID: StockCNSpaceID, DatasetID: StockCNDatasetID,
		Frequency: "1m", Provider: "sina", SourceID: "stockcn_minute_http", MarketType: "equity", RequirePeriodCommit: true,
		Items: []domain.CollectionItem{item}, Targets: []domain.WriteTarget{target, secondTarget},
	})
	require.NoError(t, err)
	require.Equal(t, "failed", payload.GetStatus(), "one accepted Dataset cannot mask a rejected required target")
	require.Len(t, payload.GetItems()[0].GetTargets(), 2)

	registry = marketdata.NewRegistry()
	recent := bar
	recent.BarStart = period.Add(time.Minute)
	recent.BarEnd = recent.BarStart.Add(time.Minute)
	require.NoError(t, registry.Register(pipelineProvider{id: "sina", rows: []marketdata.NormalizedKline{recent}}))
	router, err = marketdata.NewRouter(registry, 2, pipelineClock{now}, nil)
	require.NoError(t, err)
	missingStorage := &recordingTimerPeriodCommitStorage{}
	pipeline.Router, pipeline.Storage = router, missingStorage
	payload, err = pipeline.Execute(context.Background(), Request{
		BatchID: "missing-target", BatchKind: domain.BatchKindRealtime, SpaceID: StockCNSpaceID, DatasetID: StockCNDatasetID,
		Frequency: "1m", Provider: "sina", SourceID: "stockcn_minute_http", MarketType: "equity", RequirePeriodCommit: true,
		Items: []domain.CollectionItem{item}, Targets: []domain.WriteTarget{target},
	})
	require.NoError(t, err)
	require.Equal(t, "failed", payload.GetStatus())
	require.Equal(t, string(domain.ItemOutcomeProviderError), payload.GetItems()[0].GetOutcome())
	require.Zero(t, missingStorage.commits, "a later bar must not count as evidence for the frozen target")
}

type recordingTimerPeriodCommitStorage struct {
	recordingPeriodFailureStorage
	commits     int
	indexes     []uint32
	failDataset string
}

func (s *recordingTimerPeriodCommitStorage) CommitTimeSeriesBatch(_ context.Context, expectation *storagepb.DatasetPeriodExpectation, rows []*storagepb.TimeSeriesBatchRow, _ string) error {
	s.commits++
	s.indexes = make([]uint32, 0, len(rows))
	for _, row := range rows {
		s.indexes = append(s.indexes, row.GetSeriesIndex())
	}
	if expectation.GetDatasetId() == s.failDataset {
		return fmt.Errorf("target was not accepted")
	}
	return nil
}

type pipelineStorage struct {
	rows          []*storagepb.RowFieldUpsert
	sourceEventID string
	writes        int
}

func (s *pipelineStorage) UpsertFields(context.Context, []*storagepb.RowFieldUpsert) error {
	return nil
}
func (s *pipelineStorage) UpsertFieldsWithSource(_ context.Context, rows []*storagepb.RowFieldUpsert, source string) error {
	s.rows = rows
	s.sourceEventID = source
	s.writes++
	return nil
}

type pipelineStorageWithInstrumentNames struct {
	pipelineStorage
	names map[string]string
}

func (s *pipelineStorageWithInstrumentNames) ListInstrumentNames(_ context.Context, _ string, subjectIDs []string) (map[string]string, error) {
	result := make(map[string]string, len(subjectIDs))
	for _, subjectID := range subjectIDs {
		if name := s.names[subjectID]; name != "" {
			result[subjectID] = name
		}
	}
	return result, nil
}

func TestDefaultMarketSeriesTagDistinguishesMarketAndSource(t *testing.T) {
	require.Equal(t, "venue:binance", defaultMarketSeriesTag("binance", "binance", "spot"))
	require.Equal(t, "venue:binance|market:spot|source:spot_http", defaultMarketSeriesTag("binance", "spot_http", "spot"))
	require.Equal(t, "venue:binance|market:swap|source:swap_http", defaultMarketSeriesTag("binance", "swap_http", "swap"))
	require.Equal(t, "venue:okx", defaultMarketSeriesTag("OKX", "okx", "spot"))
	require.Equal(t, "venue:binance|market:swap", defaultMarketSeriesTag("binance", "binance", "swap"))
	require.Equal(t, "venue:binance|market:spot|source:binance-proxy", defaultMarketSeriesTag("binance", "binance-proxy", "spot"))
	require.NotEqual(t, defaultMarketSeriesTag("binance", "binance", "spot"), defaultMarketSeriesTag("binance", "binance", "swap"))
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
	payload, err := pipeline.Execute(context.Background(), Request{BatchID: "batch-1", BatchKind: domain.BatchKindBackfill, SpaceID: StockCNSpaceID, DatasetID: StockCNDatasetID, Frequency: "1m", Provider: "sina", MarketType: "equity", RequestID: "request-1", Items: []domain.CollectionItem{{SubjectID: "600000.XSHG", Symbol: "sh600000", Provider: "sina", MarketType: "equity", DataType: "kline", DatasetID: StockCNDatasetID, Frequency: "1m", StartTime: "2026-08-28T06:59:00Z", BarLimit: 3, TargetDataTime: "2026-08-28T06:59:00Z", SeriesHash: "residual-period-hash", ExpectedCount: 1}}})
	require.NoError(t, err)
	require.Equal(t, "succeeded", payload.GetStatus())
	require.Equal(t, 1, storage.writes)
	require.Equal(t, "batch-1", storage.sourceEventID)
	require.Len(t, storage.rows, 1)
	row := storage.rows[0]
	require.Equal(t, StockCNSpaceID, row.GetKey().GetSpaceId())
	require.Equal(t, StockCNDatasetID, row.GetKey().GetDatasetId())
	require.Equal(t, "default", row.GetKey().GetTimeSeries().GetSeriesTag())
	fields := make(map[string]*storagepb.TypedValue, len(row.GetFields()))
	for _, field := range row.GetFields() {
		fields[field.GetFieldId()] = field.GetValue()
	}
	require.Len(t, fields, 20)
	require.Equal(t, "600000.XSHG", fields["instrument_name"].GetStringValue())
	require.Equal(t, "sina", fields["source_provider"].GetStringValue())
	require.Equal(t, int64(1), fields["route_rank"].GetIntValue())
	require.Equal(t, "shares", fields["volume_unit"].GetStringValue())
	require.Equal(t, "reported", fields["amount_quality"].GetStringValue())
}

func TestKlinePipelineWritesOnlySelectedOutputFields(t *testing.T) {
	now := time.Date(2026, 8, 29, 2, 0, 0, 0, time.UTC)
	bar := marketdata.NormalizedKline{SubjectID: "BTC-USDT", ProviderID: "binance", ProviderSymbol: "BTCUSDT", Frequency: "1m", BarStart: time.Date(2026, 8, 28, 6, 59, 0, 0, time.UTC), BarEnd: time.Date(2026, 8, 28, 7, 0, 0, 0, time.UTC), Open: 9, High: 9.1, Low: 8.9, Close: 9.05, VolumeShares: 1000, AmountCNY: 9050, ProviderTimestamp: time.Date(2026, 8, 28, 7, 0, 0, 0, time.UTC), FetchedAt: now, RequestID: "request-selected"}
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(pipelineProvider{id: "binance", rows: []marketdata.NormalizedKline{bar}}))
	router, err := marketdata.NewRouter(registry, 1, pipelineClock{now}, func(time.Duration) {})
	require.NoError(t, err)
	storage := &pipelineStorage{}
	pipeline := &KlinePipeline{Router: router, Storage: storage, CandidateChain: []string{"binance"}, SpaceID: "crypto", MarketID: "crypto", Now: func() time.Time { return now }}
	_, err = pipeline.Execute(context.Background(), Request{BatchID: "batch-selected", BatchKind: domain.BatchKindBackfill, SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", Provider: "binance", MarketType: "spot", RequestID: "request-selected", Items: []domain.CollectionItem{{SubjectID: "BTC-USDT", Symbol: "BTCUSDT", Provider: "binance", MarketType: "spot", DataType: "kline", DatasetID: "bars", Frequency: "1m", StartTime: "2026-08-28T06:59:00Z", BarLimit: 1, OutputFields: []string{"close", "quote_volume"}}}})
	require.NoError(t, err)
	require.Len(t, storage.rows, 1)
	fields := make([]string, 0, len(storage.rows[0].GetFields()))
	for _, field := range storage.rows[0].GetFields() {
		fields = append(fields, field.GetFieldId())
	}
	require.Equal(t, []string{"close", "quote_volume"}, fields)
}

func TestKlinePipelineEmptyOutputFieldsKeepsLegacyFullRow(t *testing.T) {
	bar := marketdata.NormalizedKline{SubjectID: "BTC-USDT", ProviderID: "binance", ProviderSymbol: "BTCUSDT", Frequency: "1m", BarStart: time.Date(2026, 8, 28, 6, 59, 0, 0, time.UTC), BarEnd: time.Date(2026, 8, 28, 7, 0, 0, 0, time.UTC), Open: 9, High: 9.1, Low: 8.9, Close: 9.05, VolumeShares: 1000, AmountCNY: 9050, ProviderTimestamp: time.Date(2026, 8, 28, 7, 0, 0, 0, time.UTC), FetchedAt: time.Now().UTC(), RequestID: "request-legacy"}
	row, err := (&KlinePipeline{MarketID: "crypto"}).rowFor(bar, Request{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m"}, "route", 1)
	require.NoError(t, err)
	require.Len(t, row.GetFields(), 10)
}

func TestKlinePipelineUsesInstrumentNameFromStorageMetadata(t *testing.T) {
	now := time.Date(2026, 8, 29, 2, 0, 0, 0, time.UTC)
	bar := marketdata.NormalizedKline{SubjectID: "600000.XSHG", ProviderID: "sina", ProviderSymbol: "sh600000", Frequency: "1m", BarStart: time.Date(2026, 8, 28, 6, 59, 0, 0, time.UTC), BarEnd: time.Date(2026, 8, 28, 7, 0, 0, 0, time.UTC), Open: 9, High: 9.1, Low: 8.9, Close: 9.05, VolumeShares: 1000, AmountCNY: 9050, ProviderTimestamp: time.Date(2026, 8, 28, 7, 0, 0, 0, time.UTC), FetchedAt: now, RequestID: "request-name"}
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(pipelineProvider{id: "sina", rows: []marketdata.NormalizedKline{bar}}))
	router, err := marketdata.NewRouter(registry, 1, pipelineClock{now}, func(time.Duration) {})
	require.NoError(t, err)
	storage := &pipelineStorageWithInstrumentNames{names: map[string]string{"600000.XSHG": "浦发银行"}}
	pipeline := &KlinePipeline{Router: router, Storage: storage, CandidateChain: []string{"sina"}, Now: func() time.Time { return now }}
	_, err = pipeline.Execute(context.Background(), Request{BatchID: "batch-name", BatchKind: domain.BatchKindBackfill, SpaceID: StockCNSpaceID, DatasetID: StockCNDatasetID, Frequency: "1m", Provider: "sina", MarketType: "equity", RequestID: "request-name", Items: []domain.CollectionItem{{SubjectID: bar.SubjectID, Symbol: bar.ProviderSymbol, Provider: "sina", MarketType: "equity", DataType: "kline", DatasetID: StockCNDatasetID, Frequency: "1m", StartTime: bar.BarStart.Format(time.RFC3339), BarLimit: 1}}})
	require.NoError(t, err)
	require.Equal(t, "浦发银行", findField(storage.rows[0], "instrument_name").GetStringValue())
}

func findField(row *storagepb.RowFieldUpsert, fieldID string) *storagepb.TypedValue {
	for _, field := range row.GetFields() {
		if field.GetFieldId() == fieldID {
			return field.GetValue()
		}
	}
	return nil
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
	pipeline := &KlinePipeline{Router: router, Storage: storage, CandidateChain: []string{"binance"}, SpaceID: "crypto", MarketID: "crypto", InstrumentType: marketdata.InstrumentSpot, DatasetID: "binance_spot_kline_1h", Now: func() time.Time { return now }}
	payload, err := pipeline.Execute(context.Background(), Request{BatchID: "batch-hour", BatchKind: domain.BatchKindBackfill, SpaceID: "crypto", DatasetID: "binance_spot_kline_1h", Frequency: "1h", Provider: "binance", MarketType: "spot", RequestID: "request-hour", Items: []domain.CollectionItem{{SubjectID: bar.SubjectID, Symbol: bar.ProviderSymbol, Provider: "binance", MarketType: "spot", DataType: "kline", DatasetID: "binance_spot_kline_1h", Frequency: "1h", StartTime: barStart.Format(time.RFC3339), BarLimit: 1}}})
	require.NoError(t, err)
	require.Equal(t, "succeeded", payload.GetStatus())
	require.Len(t, storage.rows, 1)
	require.Equal(t, "1h", storage.rows[0].GetKey().GetTimeSeries().GetFreq())
}

func TestKlinePipelinePassesDNSRoutesToProvider(t *testing.T) {
	for _, marketType := range []string{"spot", "swap"} {
		t.Run(marketType, func(t *testing.T) {
			var observed marketdata.KlineRequest
			registry := marketdata.NewRegistry()
			require.NoError(t, registry.Register(pipelineProvider{id: "binance", request: &observed, err: marketdata.ErrNoClosedBar}))
			router, err := marketdata.NewRouter(registry, 2, nil, nil)
			require.NoError(t, err)
			pipeline := &KlinePipeline{Router: router, Storage: &pipelineStorage{}, CandidateChain: []string{"binance"}, SpaceID: "crypto", MarketID: "crypto", InstrumentType: marketdata.InstrumentType(marketType), DatasetID: "kline"}
			routes := map[string]sources.DNSResolution{
				"api.binance.com":  {IPs: []string{"203.0.113.1", "203.0.113.2"}},
				"fapi.binance.com": {IPs: []string{"203.0.113.3"}},
			}
			_, err = pipeline.Execute(context.Background(), Request{BatchID: "dns-hour", BatchKind: domain.BatchKindRealtime, SpaceID: "crypto", DatasetID: "kline", Frequency: "1h", Provider: "binance", MarketType: marketType, DNSRoutes: routes, Items: []domain.CollectionItem{{SubjectID: "BTC-USDT", Symbol: "BTCUSDT", Provider: "binance", MarketType: marketType, DataType: "kline", DatasetID: "kline", Frequency: "1h"}}})
			require.NoError(t, err)
			require.Equal(t, map[string][]string{"api.binance.com": {"203.0.113.1", "203.0.113.2"}, "fapi.binance.com": {"203.0.113.3"}}, observed.DNSRoutes)
			observed.DNSRoutes["api.binance.com"][0] = "203.0.113.9"
			require.Equal(t, "203.0.113.1", routes["api.binance.com"].IPs[0], "provider requests must not share mutable IP slices with the batch")
		})
	}
}

func TestRequestKlineRouteIDUsesCryptoFrequency(t *testing.T) {
	for _, test := range []struct {
		product   marketdata.InstrumentType
		frequency string
		want      string
	}{
		{product: marketdata.InstrumentSpot, frequency: "1h", want: "binance_spot_kline_1h"},
		{product: marketdata.InstrumentSwap, frequency: "1w", want: "binance_swap_kline_1w"},
	} {
		pipeline := &KlinePipeline{SpaceID: "crypto", MarketID: "crypto", InstrumentType: test.product}
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
		MarketID: "stockcn", ExchangeID: "XSHG", InstrumentType: marketdata.InstrumentEquity,
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
		MarketID: "stockcn", ExchangeID: "XSHG", InstrumentType: marketdata.InstrumentEquity,
		SubjectID: valid.SubjectID, ProviderSymbol: valid.ProviderSymbol, Frequency: "1m", Limit: 1,
		StartTime: start, EndTime: end, Now: now, RequestID: "coverage-fallback",
	}, []string{"sina", "tencent"}, 0)
	require.NoError(t, err)
	require.Equal(t, "tencent", selected)
	require.Equal(t, []marketdata.NormalizedKline{valid}, rows)
}
