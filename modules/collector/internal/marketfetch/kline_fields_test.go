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

// fakeFieldSource 是只提供附加字段的假来源：values 给出每根的值，err 非空时取数失败；记下每次请求。
type fakeFieldSource struct {
	id     string
	specs  []marketdata.FieldSpec
	values func(marketdata.FieldRequest) map[time.Time]map[string]marketdata.FieldValue
	err    error

	mu       sync.Mutex
	requests []marketdata.FieldRequest
}

func (s *fakeFieldSource) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{ID: s.id}
}
func (s *fakeFieldSource) FieldSpecs() []marketdata.FieldSpec { return s.specs }
func (s *fakeFieldSource) FetchFields(_ context.Context, req marketdata.FieldRequest) (map[time.Time]map[string]marketdata.FieldValue, error) {
	s.mu.Lock()
	s.requests = append(s.requests, req)
	s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	return s.values(req), nil
}
func (s *fakeFieldSource) requested() []marketdata.FieldRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]marketdata.FieldRequest(nil), s.requests...)
}

// everyHour 给 [Start, End) 内每个整点的根都返回 value(第几根)。
func everyHour(fieldID string, value func(index int) marketdata.FieldValue) func(marketdata.FieldRequest) map[time.Time]map[string]marketdata.FieldValue {
	return func(req marketdata.FieldRequest) map[time.Time]map[string]marketdata.FieldValue {
		out := map[time.Time]map[string]marketdata.FieldValue{}
		for index, at := 0, req.Start; at.Before(req.End); index, at = index+1, at.Add(time.Hour) {
			out[at] = map[string]marketdata.FieldValue{fieldID: value(index)}
		}
		return out
	}
}

func adjSource() *fakeFieldSource {
	return &fakeFieldSource{id: "fake_adj", specs: []marketdata.FieldSpec{{ID: "adj_factor", Type: marketdata.FieldTypeDouble, Fill: "prev", Frequencies: []string{"1h"}}},
		values: everyHour("adj_factor", func(index int) marketdata.FieldValue { return marketdata.DoubleField(1.5 + float64(index)) })}
}

func industrySource() *fakeFieldSource {
	return &fakeFieldSource{id: "fake_industry", specs: []marketdata.FieldSpec{{ID: "sw_l1", Type: marketdata.FieldTypeInt, Fill: "prev", Frequencies: []string{"1h"}}},
		values: everyHour("sw_l1", func(int) marketdata.FieldValue { return marketdata.IntField(480000) })}
}

// fieldsRequest 是一次补采：每个写入目标各选各的输出字段。
func fieldsRequest(targets map[string]string) Request {
	req := fundingBackfillRequest(domain.BatchKindBackfill)
	req.Targets = nil
	for datasetID, fields := range targets {
		req.Targets = append(req.Targets, domain.WriteTarget{ID: "t-" + datasetID, InstanceID: "i1", DatasetID: datasetID, OutputFields: fields})
	}
	return req
}

func fieldsPipeline(t *testing.T, storage Storage, sources ...marketdata.FieldSource) *KlinePipeline {
	t.Helper()
	pipeline := fundingPipeline(t, swapHourBars(3), nil, storage)
	pipeline.Fields = sources
	pipeline.ExtraFieldIDs = []string{"adj_factor", "sw_l1"}
	return pipeline
}

// 两个来源各出各的字段：选了的目标写入每根的值，没选的目标不出现这些列；每个来源只被问到自己的字段，区间是本块的覆盖范围。
func TestKlinePipelineFillsFieldsFromTwoSources(t *testing.T) {
	adj, industry := adjSource(), industrySource()
	storage := &datasetStorage{}
	payload, err := fieldsPipeline(t, storage, adj, industry).Execute(context.Background(), fieldsRequest(map[string]string{
		"all": `["close","adj_factor","sw_l1"]`, "plain": `["close"]`,
	}))
	require.NoError(t, err)
	require.Equal(t, "succeeded", payload.GetStatus())
	require.Len(t, storage.rows["all"], 3)
	for index, row := range storage.rows["all"] {
		require.Equal(t, 1.5+float64(index), findField(row, "adj_factor").GetDoubleValue())
		require.Equal(t, int64(480000), findField(row, "sw_l1").GetIntValue())
	}
	require.Len(t, storage.rows["plain"], 3)
	for _, row := range storage.rows["plain"] {
		require.Len(t, row.GetFields(), 1, "没选附加字段的目标只有 close")
	}
	requests := adj.requested()
	require.Len(t, requests, 1)
	require.Equal(t, []string{"adj_factor"}, requests[0].Fields)
	require.Equal(t, fundingBarBase, requests[0].Start)
	require.Equal(t, fundingBarBase.Add(3*time.Hour), requests[0].End)
	require.Equal(t, "BTCUSDT", requests[0].ProviderSymbol)
	require.False(t, requests[0].Realtime)
	require.Equal(t, []string{"sw_l1"}, industry.requested()[0].Fields)
}

// 一个来源报错：只有选了它的字段的目标失败，错误归为可重试的 provider_error；选了另一个来源的目标和没选的目标照常写。
func TestKlinePipelineFieldSourceErrorFailsOnlyTargetsThatSelectedIt(t *testing.T) {
	adj, industry := adjSource(), industrySource()
	adj.err = errors.New("上游暂时不可用")
	storage := &datasetStorage{}
	payload, err := fieldsPipeline(t, storage, adj, industry).Execute(context.Background(), fieldsRequest(map[string]string{
		"with_adj": `["close","adj_factor"]`, "with_industry": `["close","sw_l1"]`, "plain": `["close"]`,
	}))
	require.NoError(t, err)
	require.Len(t, payload.GetItems(), 1)
	require.Equal(t, string(domain.ItemOutcomeProviderError), payload.GetItems()[0].GetOutcome())
	require.Equal(t, "adj_factor", payload.GetItems()[0].GetErrorType())
	require.Equal(t, int32(1), payload.GetRetryCount(), "可以重试")
	require.Empty(t, storage.rows["with_adj"])
	require.Len(t, storage.rows["with_industry"], 3)
	require.NotNil(t, findField(storage.rows["with_industry"][0], "sw_l1"))
	require.Len(t, storage.rows["plain"], 3)
}

// 来源正常返回但某根没有值：照常写行，这一列留空，不算失败。
func TestKlinePipelineWritesRowWhenFieldHasNoValue(t *testing.T) {
	adj, industry := adjSource(), industrySource()
	adj.values = func(req marketdata.FieldRequest) map[time.Time]map[string]marketdata.FieldValue {
		return map[time.Time]map[string]marketdata.FieldValue{req.Start: {"adj_factor": marketdata.DoubleField(2)}}
	}
	industry.values = func(marketdata.FieldRequest) map[time.Time]map[string]marketdata.FieldValue { return nil }
	storage := &datasetStorage{}
	payload, err := fieldsPipeline(t, storage, adj, industry).Execute(context.Background(), fieldsRequest(map[string]string{"all": `["close","adj_factor","sw_l1"]`}))
	require.NoError(t, err)
	require.Equal(t, "succeeded", payload.GetStatus())
	rows := storage.rows["all"]
	require.Len(t, rows, 3)
	require.Equal(t, 2.0, findField(rows[0], "adj_factor").GetDoubleValue())
	require.Nil(t, findField(rows[1], "adj_factor"), "没有值的根这一列为空")
	require.Nil(t, findField(rows[2], "adj_factor"))
	for _, row := range rows {
		require.NotNil(t, findField(row, "close"))
		require.Nil(t, findField(row, "sw_l1"))
	}
}

// 没有目标选附加字段时不取数。
func TestKlinePipelineSkipsFieldSourcesWhenNotSelected(t *testing.T) {
	adj, industry := adjSource(), industrySource()
	storage := &datasetStorage{}
	payload, err := fieldsPipeline(t, storage, adj, industry).Execute(context.Background(), fieldsRequest(map[string]string{"plain": `["close"]`, "default": ""}))
	require.NoError(t, err)
	require.Equal(t, "succeeded", payload.GetStatus())
	require.Empty(t, adj.requested())
	require.Empty(t, industry.requested())
	require.Len(t, storage.rows["plain"], 3)
}

// 区间不完整（没给结束时间）时等 K 线取完，按它们覆盖的范围取附加字段。
func TestKlinePipelineFetchesFieldsAfterKlinesWhenRangeUnknown(t *testing.T) {
	adj := adjSource()
	storage := &datasetStorage{}
	req := fieldsRequest(map[string]string{"all": `["close","adj_factor"]`})
	req.Items[0].EndTime = ""
	payload, err := fieldsPipeline(t, storage, adj).Execute(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, "succeeded", payload.GetStatus())
	requests := adj.requested()
	require.Len(t, requests, 1)
	require.Equal(t, fundingBarBase, requests[0].Start)
	require.Equal(t, fundingBarBase.Add(3*time.Hour), requests[0].End)
	require.Len(t, storage.rows["all"], 3)
}

// A 股专用的行与通用的行都输出附加字段，按字段名排序，整数与浮点各用各的类型。
func TestKlineRowsCarryExtraFields(t *testing.T) {
	extra := map[string]marketdata.FieldValue{"sw_l1": marketdata.IntField(480000), "adj_factor": marketdata.DoubleField(7.5)}
	assertExtra := func(row *storagepb.RowFieldUpsert) {
		fields := row.GetFields()
		require.Equal(t, "adj_factor", fields[len(fields)-2].GetFieldId())
		require.Equal(t, 7.5, fields[len(fields)-2].GetValue().GetDoubleValue())
		require.Equal(t, "sw_l1", fields[len(fields)-1].GetFieldId())
		require.Equal(t, int64(480000), fields[len(fields)-1].GetValue().GetIntValue())
	}
	crypto := swapHourBars(1)[0]
	crypto.Extra = extra
	row, err := (&KlineCommitter{MarketID: "crypto", InstrumentType: marketdata.InstrumentSwap}).rowFor(crypto, Request{SpaceID: "crypto", DatasetID: "bars", Frequency: "1h", MarketType: "swap"}, "route", 1)
	require.NoError(t, err)
	assertExtra(row)

	start := time.Date(2026, 11, 10, 1, 30, 0, 0, time.UTC)
	stock := marketdata.NormalizedKline{SubjectID: "600519.XSHG", ProviderID: "tdx", ProviderSymbol: "sh600519", Frequency: "5m",
		BarStart: start, BarEnd: start.Add(5 * time.Minute), Open: 10, High: 11, Low: 9, Close: 10.5, Volume: 100, Amount: 1050,
		ProviderTimestamp: start.Add(5 * time.Minute), FetchedAt: start.Add(6 * time.Minute), RequestID: "r", Extra: extra}
	row, err = stockKlineRow(stock, "bars", "route", 1, "贵州茅台")
	require.NoError(t, err)
	assertExtra(row)
}

// 资金费率：区间里放不下完整一根时报错，不能当成“没有值”写成空列。
func TestFundingFetchFieldsRejectsBarOutsideQueriedRange(t *testing.T) {
	cache := NewFundingCache(&fakeFundingSource{}, nil)
	req := marketdata.FieldRequest{ProviderSymbol: "BTCUSDT", Frequency: "1h", Start: fundingBarBase, End: fundingBarBase.Add(90 * time.Minute)}
	_, err := cache.FetchFields(context.Background(), req)
	require.ErrorContains(t, err, "超出资金费率查询区间")

	req.End = fundingBarBase.Add(2 * time.Hour)
	values, err := cache.FetchFields(context.Background(), req)
	require.NoError(t, err)
	require.Len(t, values, 2)
	require.Equal(t, marketdata.DoubleField(0), values[fundingBarBase][domain.FieldFundingRate], "本根没有结算写 0")
}

// 字段来源的运行参数按提供方 ID 从数据源配置文件加载，不要求它出现在路由里；没有配置文件的来源用默认值。
func TestLoadSourceRuntimeByProviderID(t *testing.T) {
	writeMarketFile(t, t.TempDir(), "stockcn/sources/fake_hfq.yaml", `provider:
  id: fake_hfq
  market: stockcn
  hosts: [finance.example.com]
  rate_limit:
    scope: egress_ip
    requests_per_second: 3
    burst: 2
    max_concurrent: 2
    cooldown: 1s
    request_timeout: 4s
`)
	config, found, err := LoadSourceRuntime("stockcn", "fake_hfq")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, 3.0, config.RateLimit.RequestsPerSecond)
	require.Equal(t, 4*time.Second, config.RateLimit.RequestTimeout)
	require.Equal(t, []string{"finance.example.com"}, config.Hosts)

	_, found, err = LoadSourceRuntime("stockcn", "no_such_source")
	require.NoError(t, err)
	require.False(t, found)
}
