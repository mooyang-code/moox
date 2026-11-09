package marketfetch

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

// datasetStorage 按数据集记下写入的行。
type datasetStorage struct {
	mu   sync.Mutex
	rows map[string][]*storagepb.RowFieldUpsert
}

func (s *datasetStorage) UpsertFields(_ context.Context, rows []*storagepb.RowFieldUpsert) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rows == nil {
		s.rows = map[string][]*storagepb.RowFieldUpsert{}
	}
	for _, row := range rows {
		s.rows[row.GetKey().GetDatasetId()] = append(s.rows[row.GetKey().GetDatasetId()], row)
	}
	return nil
}

var fundingBarBase = time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)

func swapHourBars(count int) []marketdata.NormalizedKline {
	bars := make([]marketdata.NormalizedKline, 0, count)
	for index := 0; index < count; index++ {
		start := fundingBarBase.Add(time.Duration(index) * time.Hour)
		bars = append(bars, marketdata.NormalizedKline{SubjectID: "BTC-USDT", ProviderID: "binance", SourceID: "swap_http", ProviderSymbol: "BTCUSDT", Frequency: "1h",
			BarStart: start, BarEnd: start.Add(time.Hour), Open: 100, High: 110, Low: 90, Close: 105, VolumeShares: 12, AmountCNY: 1234,
			ProviderTimestamp: start.Add(time.Hour), FetchedAt: start.Add(2 * time.Hour), RequestID: "funding"})
	}
	return bars
}

func fundingPipeline(t *testing.T, bars []marketdata.NormalizedKline, funding *FundingCache, storage Storage) *KlinePipeline {
	t.Helper()
	now := fundingBarBase.Add(24 * time.Hour)
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(pipelineProvider{id: "binance", sourceID: "swap_http", rows: bars}))
	router, err := marketdata.NewRouter(registry, 1, pipelineClock{now}, func(time.Duration) {})
	require.NoError(t, err)
	return &KlinePipeline{Router: router, Storage: storage, CandidateChain: []string{"binance"}, SpaceID: "crypto", MarketID: "crypto",
		InstrumentType: marketdata.InstrumentSwap, SourceID: "swap_http", Now: func() time.Time { return now }, Funding: funding}
}

// 补采两个目标：选了资金费率的目标写入每根的区间和，另一个目标不出现这一列。
func fundingBackfillRequest(kind domain.BatchKind) Request {
	item := domain.CollectionItem{InstanceID: "i1", SubjectID: "BTC-USDT", Symbol: "BTCUSDT", Provider: "binance", SourceID: "swap_http", MarketType: "swap", DataType: "kline",
		DatasetID: "with_funding", Frequency: "1h", StartTime: fundingBarBase.Format(time.RFC3339), EndTime: fundingBarBase.Add(3 * time.Hour).Format(time.RFC3339), BarLimit: 3}
	return Request{BatchID: "funding-batch", BatchKind: kind, SpaceID: "crypto", DatasetID: "with_funding", Frequency: "1h", Provider: "binance", SourceID: "swap_http", MarketType: "swap", PlanRequest: true,
		Items: []domain.CollectionItem{item},
		Targets: []domain.WriteTarget{
			{ID: "t-funding", InstanceID: "i1", DatasetID: "with_funding", OutputFields: `["close","funding_rate"]`},
			{ID: "t-plain", InstanceID: "i1", DatasetID: "plain", OutputFields: `["close"]`},
		}}
}

func fundingValues(rows []*storagepb.RowFieldUpsert) []float64 {
	var values []float64
	for _, row := range rows {
		if value := findField(row, domain.FieldFundingRate); value != nil {
			values = append(values, value.GetDoubleValue())
		}
	}
	return values
}

// 一根内 0、1、2 次结算；结算晚 1 毫秒仍归本根；下一根开盘时刻的结算不算进上一根。
func TestKlinePipelineBackfillFillsFundingRatePerBar(t *testing.T) {
	source := &fakeFundingSource{rates: []marketdata.FundingRate{
		fundingAt("BTCUSDT", fundingBarBase.Add(time.Hour+time.Millisecond), 0.0001),
		fundingAt("BTCUSDT", fundingBarBase.Add(2*time.Hour), 0.0001),
		fundingAt("BTCUSDT", fundingBarBase.Add(3*time.Hour-time.Millisecond), 0.0002),
		fundingAt("BTCUSDT", fundingBarBase.Add(3*time.Hour), 0.0009),
		fundingAt("ETHUSDT", fundingBarBase.Add(time.Hour), 0.0005),
	}}
	storage := &datasetStorage{}
	pipeline := fundingPipeline(t, swapHourBars(3), NewFundingCache(source, nil), storage)
	payload, err := pipeline.Execute(context.Background(), fundingBackfillRequest(domain.BatchKindBackfill))
	require.NoError(t, err)
	require.Equal(t, "succeeded", payload.GetStatus())
	values := fundingValues(storage.rows["with_funding"])
	require.Len(t, values, 3)
	require.Equal(t, 0.0, values[0])
	require.Equal(t, 0.0001, values[1])
	require.InDelta(t, 0.0003, values[2], 1e-12)
	require.Len(t, storage.rows["plain"], 3)
	require.Empty(t, fundingValues(storage.rows["plain"]), "没选这一列的目标不写资金费率")
	queries := source.queried()
	require.Len(t, queries, 1)
	require.Equal(t, fundingQuery{symbol: "BTCUSDT", start: fundingBarBase, end: fundingBarBase.Add(3 * time.Hour)}, queries[0], "补采按合约查本块区间，补采优先级")
}

// 取不到资金费：只让选了这一列的目标失败，错误归为可重试的 provider_error；另一个目标照常写入。
func TestKlinePipelineFundingFailureFailsOnlyFundingTarget(t *testing.T) {
	source := &fakeFundingSource{failures: 1}
	storage := &datasetStorage{}
	pipeline := fundingPipeline(t, swapHourBars(3), NewFundingCache(source, nil), storage)
	payload, err := pipeline.Execute(context.Background(), fundingBackfillRequest(domain.BatchKindBackfill))
	require.NoError(t, err)
	require.Len(t, payload.GetItems(), 1)
	require.Equal(t, string(domain.ItemOutcomeProviderError), payload.GetItems()[0].GetOutcome())
	require.Equal(t, "funding_rate", payload.GetItems()[0].GetErrorType())
	require.Equal(t, int32(1), payload.GetRetryCount(), "可以重试")
	require.Empty(t, storage.rows["with_funding"])
	require.Len(t, storage.rows["plain"], 3)
}

// 实时批次读进程缓存：缺的小时槽按全市场查询补取，走实时优先级。
func TestKlinePipelineRealtimeReadsFundingCache(t *testing.T) {
	source := &fakeFundingSource{rates: []marketdata.FundingRate{fundingAt("BTCUSDT", fundingBarBase.Add(2*time.Hour), 0.0004)}}
	storage := &datasetStorage{}
	bars := swapHourBars(3)[2:]
	pipeline := fundingPipeline(t, bars, NewFundingCache(source, nil), storage)
	req := fundingBackfillRequest(domain.BatchKindRealtime)
	req.Items[0].StartTime = bars[0].BarStart.Format(time.RFC3339)
	req.Items[0].BarLimit = 1
	payload, err := pipeline.Execute(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, "succeeded", payload.GetStatus())
	require.Equal(t, []float64{0.0004}, fundingValues(storage.rows["with_funding"]))
	queries := source.queried()
	require.Len(t, queries, 1)
	require.Empty(t, queries[0].symbol)
	require.True(t, queries[0].realtime)
}

// 没有写入目标、取数项自己选了资金费率，而行情源没有这项能力：配置错误，不重试。
func TestKlinePipelineFundingWithoutCapabilityIsInvalid(t *testing.T) {
	storage := &datasetStorage{}
	pipeline := fundingPipeline(t, swapHourBars(1), nil, storage)
	req := fundingBackfillRequest(domain.BatchKindBackfill)
	req.Targets = nil
	req.Items[0].OutputFields = []string{"close", domain.FieldFundingRate}
	payload, err := pipeline.Execute(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, string(domain.ItemOutcomeInvalid), payload.GetItems()[0].GetOutcome())
	require.Empty(t, storage.rows)
}

// 输出字段为空（默认字段）时不包含资金费率，选中时才保留。
func TestSelectOutputFieldsKeepsFundingRateOnlyWhenSelected(t *testing.T) {
	fields := []*storagepb.FieldValue{doubleValue("close", 1), doubleValue(domain.FieldFundingRate, 0.0001)}
	require.Len(t, selectOutputFields(fields, nil), 1)
	require.Equal(t, "close", selectOutputFields(fields, nil)[0].GetFieldId())
	require.Len(t, selectOutputFields(fields, []string{"close", domain.FieldFundingRate}), 2)
	plain := []*storagepb.FieldValue{doubleValue("close", 1)}
	require.Same(t, plain[0], selectOutputFields(plain, nil)[0])
}

func TestFundingFailureClassification(t *testing.T) {
	item := domain.CollectionItem{SubjectID: "BTC-USDT"}
	require.Equal(t, domain.ItemOutcomeProviderError, fundingFailure(item, context.DeadlineExceeded).Outcome, "等待超时也归为 provider_error")
	require.Equal(t, domain.ItemOutcomeProviderError, fundingFailure(item, errors.New("x")).Outcome)
	require.Equal(t, domain.ItemOutcomeInvalid, fundingFailure(item, errFundingUnsupported).Outcome)
}
