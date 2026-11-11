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

// fundingFieldSource 把资金费率缓存接成附加字段来源（生产里由币安永续适配器担任）。
type fundingFieldSource struct{ *FundingCache }

func (fundingFieldSource) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{ID: "binance", SourceID: "swap_http"}
}
func (fundingFieldSource) FieldSpecs() []marketdata.FieldSpec { return FundingFieldSpecs() }

func fundingPipeline(t *testing.T, bars []marketdata.NormalizedKline, funding *FundingCache, storage Storage) *KlinePipeline {
	t.Helper()
	var fields []marketdata.FieldSource
	if funding != nil {
		fields = []marketdata.FieldSource{fundingFieldSource{funding}}
	}
	now := fundingBarBase.Add(24 * time.Hour)
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(pipelineProvider{id: "binance", sourceID: "swap_http", rows: bars}))
	router, err := marketdata.NewRouter(registry, 1, pipelineClock{now}, func(time.Duration) {})
	require.NoError(t, err)
	return &KlinePipeline{Router: router, Storage: storage, CandidateChain: []string{"binance"}, SpaceID: "crypto", MarketID: "crypto",
		InstrumentType: marketdata.InstrumentSwap, SourceID: "swap_http", Now: func() time.Time { return now }, Fields: fields, ExtraFieldIDs: []string{domain.FieldFundingRate}}
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

type blockingFundingSource struct{}

func (blockingFundingSource) FundingRates(ctx context.Context, _ string, _, _ time.Time) ([]marketdata.FundingRate, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// 实时批次等缓存有一个早于批次截止的上限：等不到只判选了资金费率的目标失败，另一个目标照常写入。
func TestKlinePipelineRealtimeFundingWaitIsBoundedBeforeDeadline(t *testing.T) {
	storage := &datasetStorage{}
	bars := swapHourBars(1)
	pipeline := fundingPipeline(t, bars, NewFundingCache(blockingFundingSource{}, nil), storage)
	req := fundingBackfillRequest("")
	req.Items[0].EndTime = bars[0].BarEnd.Format(time.RFC3339)
	req.Items[0].BarLimit = 1
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	payload, err := pipeline.Execute(ctx, req)
	require.NoError(t, err)
	require.Less(t, time.Since(started), 900*time.Millisecond, "在批次截止前就停止等待")
	require.Equal(t, string(domain.ItemOutcomeProviderError), payload.GetItems()[0].GetOutcome())
	require.Empty(t, storage.rows["with_funding"])
	require.Len(t, storage.rows["plain"], 1)
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
	extra := map[string]marketdata.FieldValue{domain.FieldFundingRate: marketdata.DoubleField(0.0001)}
	require.Len(t, selectOutputFields(fields, nil, extra), 1)
	require.Equal(t, "close", selectOutputFields(fields, nil, extra)[0].GetFieldId())
	require.Len(t, selectOutputFields(fields, []string{"close", domain.FieldFundingRate}, extra), 2)
	plain := []*storagepb.FieldValue{doubleValue("close", 1)}
	require.Same(t, plain[0], selectOutputFields(plain, nil, nil)[0])
}

// 任务选的资金费率要原样进到计划与计划构造的请求里，执行端才知道要取这一列。
func TestDesiredPlanCarriesFundingRateIntoRequests(t *testing.T) {
	groups := testGroups(3, "1h")
	groups[0].OutputFields = []string{"close", domain.FieldFundingRate}
	state, err := BuildDesiredState(testPlanOptions(), groups, nil)
	require.NoError(t, err)
	require.NotEmpty(t, state.Functions)
	for _, function := range state.Functions {
		dataset := findPlanDataset(function.Plan, "ds_spot_1h|1h")
		require.NotNil(t, dataset)
		require.Contains(t, dataset.OutputFields, domain.FieldFundingRate)
		period := time.Unix(dataset.Copies[0].From, 0).UTC()
		planned, ok, err := function.Plan.buildFrequencyRequest("crypto", "f", "r", "1h", marketdata.FrequencyHour, period)
		require.NoError(t, err)
		require.True(t, ok)
		for _, item := range planned.Request.Items {
			require.Contains(t, selectedOutputFields(planned.Request, item), domain.FieldFundingRate)
		}
	}
}

// 推送的周期提交：数据集选了资金费率时写入这一列；有一根缺值就整批报错，不能写出缺列的行。
func TestPeriodRowsWritesFundingRateAndRejectsMissingValue(t *testing.T) {
	period := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	plan := swapTestPlan(period)
	plan.Datasets[0].OutputFields = []string{"close", domain.FieldFundingRate}
	committer := NewKlineCommitter(&capturingPeriodStorage{}, plan.Market)
	btc := testSwapBar("BTC-USDT", "", period, 1)
	btc.Extra = map[string]marketdata.FieldValue{domain.FieldFundingRate: marketdata.DoubleField(0.0001)}
	eth := testSwapBar("ETH-USDT", "", period, 2)
	eth.Extra = map[string]marketdata.FieldValue{domain.FieldFundingRate: marketdata.DoubleField(0)}
	batch := KlineBatch{SpaceID: "crypto", BatchID: "b", Market: plan.Market, Dataset: plan.Datasets[0], Period: period, Symbols: plan.ExchangeSymbol, Bars: []marketdata.NormalizedKline{btc, eth}}
	_, rows, _, err := committer.periodRows(batch)
	require.NoError(t, err)
	require.Equal(t, []float64{0.0001, 0}, fundingValues(rows))

	eth.Extra = nil
	batch.Bars = []marketdata.NormalizedKline{btc, eth}
	_, _, _, err = committer.periodRows(batch)
	require.ErrorContains(t, err, "缺资金费率")

	// 不选这一列时，即使 K 线带了值也不写。
	plan.Datasets[0].OutputFields = nil
	batch.Dataset = plan.Datasets[0]
	_, rows, _, err = committer.periodRows(batch)
	require.NoError(t, err)
	require.Empty(t, fundingValues(rows))
}

func TestFundingFailureClassification(t *testing.T) {
	item := domain.CollectionItem{SubjectID: "BTC-USDT"}
	require.Equal(t, domain.ItemOutcomeProviderError, fieldFailure(item, domain.FieldFundingRate, context.DeadlineExceeded).Outcome, "等待超时也归为 provider_error")
	require.Equal(t, domain.ItemOutcomeProviderError, fieldFailure(item, domain.FieldFundingRate, errors.New("x")).Outcome)
	require.Equal(t, domain.ItemOutcomeInvalid, fieldFailure(item, domain.FieldFundingRate, errFieldUnsupported).Outcome)
}
