package marketwiring

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/mooyang-code/moox/modules/collector/internal/model"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/catalog"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/markethttp"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

func TestNewRoutedProviderCarriesSourceIdentity(t *testing.T) {
	provider, err := newRoutedProvider("stockcn", marketdata.InstrumentEquity, "sina", "stockcn_minute_http", marketfetch.SourceRuntime{
		KlineSpec: marketfetch.SourceKlineConfig{MaxBarsPerRequest: 1023},
		RateLimit: marketdata.RateLimitPolicy{
			RequestsPerSecond: 5, Burst: 2, MaxConcurrent: 1,
			Cooldown: time.Second, RequestTimeout: time.Second,
		},
	})
	require.NoError(t, err)
	require.Equal(t, "sina", provider.Descriptor().ID)
	require.Equal(t, "stockcn_minute_http", provider.Descriptor().SourceID)
}

func TestNewRoutedProviderSupportsNormalTDX(t *testing.T) {
	provider, err := newRoutedProvider("stockcn", marketdata.InstrumentEquity, "tdx", "normal_7709", marketfetch.SourceRuntime{
		Hosts:     []string{"quotes.example"},
		Port:      7709,
		KlineSpec: marketfetch.SourceKlineConfig{MaxBarsPerRequest: 800},
	})
	require.NoError(t, err)
	require.Equal(t, "tdx", provider.Descriptor().ID)
	require.Equal(t, "normal_7709", provider.Descriptor().SourceID)
}

// 来源按路由分配的（市场，品种）：候选链按频率取路由，不看请求里的数据源；结算延迟取路由文件。
func TestNewKlinePipelineUsesTheFrequencyRoute(t *testing.T) {
	minute, err := NewKlinePipeline(timerHandlerStorage{}, "stockcn", marketdata.InstrumentEquity, "1m", "eastmoney", nil)
	require.NoError(t, err)
	require.Equal(t, []string{"sina", "tencent", "tdx", "eastmoney"}, minute.CandidateChain)
	require.True(t, minute.AutoBindSource)
	require.Empty(t, minute.SourceID)
	require.Equal(t, 800*time.Millisecond, minute.SettleDelay)

	five, err := NewKlinePipeline(timerHandlerStorage{}, "stockcn", marketdata.InstrumentEquity, "5m", "", nil)
	require.NoError(t, err)
	require.Equal(t, []string{"tdx", "eastmoney", "sina", "tencent"}, five.CandidateChain)

	_, err = NewKlinePipeline(timerHandlerStorage{}, "stockcn", marketdata.InstrumentEquity, "15m", "", nil)
	require.ErrorContains(t, err, "没有 15m 的 K 线路由")
}

// 单一来源的（市场，品种）：数据源是请求指定的那一个，source_id 取行情源目录登记的。
func TestNewKlinePipelineBindsTheSingleSource(t *testing.T) {
	for _, tc := range []struct {
		marketID   string
		instrument marketdata.InstrumentType
		provider   string
		sourceID   string
	}{
		{"crypto", marketdata.InstrumentSpot, "binance", "spot_http"},
		{"crypto", marketdata.InstrumentSwap, "binance", "swap_http"},
		{"stockcn", marketdata.InstrumentIndex, "csindex", "index_http"},
		{"stockcn", marketdata.InstrumentIndex, "cni", "index_cni_http"},
		{"stockcn", marketdata.InstrumentConvertibleBond, "eastmoney", "convertible_bond_http"},
	} {
		pipeline, err := NewKlinePipeline(timerHandlerStorage{}, tc.marketID, tc.instrument, "1d", tc.provider, nil)
		require.NoError(t, err, "%+v", tc)
		require.Equal(t, tc.marketID, pipeline.SpaceID)
		require.Equal(t, tc.marketID, pipeline.MarketID)
		require.Equal(t, tc.instrument, pipeline.InstrumentType)
		require.Equal(t, tc.sourceID, pipeline.SourceID)
		require.Equal(t, []string{tc.provider}, pipeline.CandidateChain)
		require.False(t, pipeline.AutoBindSource)
		require.Zero(t, pipeline.SettleDelay)
		require.Empty(t, pipeline.DatasetID, "数据集取请求里的")
	}
}

// 合并两个构造函数后，币安的限速仍然来自请求带的限速：每秒请求数、突发、并发、超时都交给数据源，熔断阈值按并发算。
func TestNewKlinePipelineKeepsTheRequestLimitsForTheSingleSource(t *testing.T) {
	t.Setenv("MOOX_FETCH_FEED_CONCURRENCY", "")
	t.Setenv("MOOX_FETCH_HTTP_MAX_ATTEMPTS", "")
	limits := &marketfetch.FeedLimits{RPS: 40, Burst: 60, FeedConcurrency: 16, RequestTimeoutMS: 3000}
	for _, instrument := range []marketdata.InstrumentType{marketdata.InstrumentSpot, marketdata.InstrumentSwap} {
		pipeline, err := NewKlinePipeline(timerHandlerStorage{}, "crypto", instrument, "1m", "binance", limits)
		require.NoError(t, err)
		spec, err := pipeline.Router.NewSession().KlineSpec("binance")
		require.NoError(t, err)
		require.Equal(t, 40.0, spec.RateLimit.RequestsPerSecond)
		require.Equal(t, 60, spec.RateLimit.Burst)
		require.Equal(t, 16, spec.RateLimit.MaxConcurrent)
		require.Equal(t, 3*time.Second, spec.RateLimit.RequestTimeout)
		require.Equal(t, marketfetch.KlineProviderAttemptBudget*16, pipeline.Router.BreakerThreshold())

		// 不带限速时数据源用自己的默认值，熔断阈值按进程环境（没设并发按 1）。
		plain, err := NewKlinePipeline(timerHandlerStorage{}, "crypto", instrument, "1m", "binance", nil)
		require.NoError(t, err)
		spec, err = plain.Router.NewSession().KlineSpec("binance")
		require.NoError(t, err)
		require.NotEqual(t, 40.0, spec.RateLimit.RequestsPerSecond)
		require.Equal(t, marketfetch.KlineProviderAttemptBudget, plain.Router.BreakerThreshold())
	}
}

func TestNewKlinePipelineRejectsSourcesMissingFromTheCatalog(t *testing.T) {
	for _, tc := range []struct {
		marketID   string
		instrument marketdata.InstrumentType
		provider   string
	}{
		{"crypto", marketdata.InstrumentSwap, "unknown"},
		{"crypto", marketdata.InstrumentSwap, ""},
		{"crypto", marketdata.InstrumentEquity, "binance"},
		{"crypto", marketdata.InstrumentIndex, "sina"},
		{"stockcn", marketdata.InstrumentConvertibleBond, "tencent"},
		{"stockcn", "futures", "eastmoney"},
		{"stockus", marketdata.InstrumentEquity, "eastmoney"},
	} {
		_, err := NewKlinePipeline(timerHandlerStorage{}, tc.marketID, tc.instrument, "1d", tc.provider, nil)
		require.ErrorContains(t, err, "行情源目录里没有", "%+v", tc)
	}
	_, err := NewKlinePipeline(nil, "crypto", marketdata.InstrumentSpot, "1m", "binance", nil)
	require.ErrorContains(t, err, "缺少 Storage")
}

// 行情源目录里“按路由分配”的登记与各市场路由文件写的品种必须一致：装配看目录，请求来源、路由 ID、结算延迟校验看路由文件。
func TestCatalogRoutingMatchesTheRouteFiles(t *testing.T) {
	seen := map[string]bool{}
	for _, entry := range catalog.Default().Entries() {
		key := string(entry.MarketID) + "/" + string(entry.Instrument)
		if seen[key] {
			continue
		}
		seen[key] = true
		_, found, err := marketfetch.RouteFor(string(entry.MarketID), entry.Instrument)
		require.NoError(t, err, key)
		require.Equal(t, catalog.Default().Routed(entry.MarketID, entry.Instrument), found, key)
	}
	// 有路由文件的市场，路由管的品种在目录里必须有按路由分配的登记项。
	for _, marketID := range markets.IDs() {
		route, found, err := marketfetch.LoadRoute(marketID)
		require.NoError(t, err, marketID)
		if found {
			require.True(t, catalog.Default().Routed(marketdata.MarketID(marketID), route.Instrument()), marketID)
		}
	}
}

// klineServer 是返回东方财富格式 K 线的测试服务：每个时间标签一根，标签是开盘时刻（上海时间）。
func klineServer(t *testing.T, labels ...string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rows := make([]string, 0, len(labels))
		for index, label := range labels {
			rows = append(rows, fmt.Sprintf(`"%s,%d,%d.5,%d.8,%d,1200,345600"`, label, 100+index, 100+index, 100+index, 100+index))
		}
		_, _ = fmt.Fprintf(w, `{"data":{"klines":[%s]}}`, strings.Join(rows, ","))
	}))
	t.Cleanup(server.Close)
	return server
}

// releasedSource 是已放行（enabled）的 A 股单一来源数据源，与目录里指数、可转债用的是同一个实现（markethttp），指向测试服务。
func releasedSource(server *httptest.Server, providerID, sourceID string, instrument marketdata.InstrumentType, frequency string, now func() time.Time) marketdata.MarketProvider {
	return markethttp.New(markethttp.Config{
		ProviderID: providerID, SourceID: sourceID, DisplayName: providerID, MarketID: "stockcn", InstrumentType: instrument,
		Exchanges: []string{"XSHG", "XSHE"}, BaseURL: server.URL, Endpoint: "/kline", Host: "fixture.test", HTTPClient: server.Client(),
		Location: time.FixedZone("Asia/Shanghai", 8*60*60), SymbolFunc: func(string) (string, error) { return "0.000001", nil },
		Frequencies: []string{frequency}, MaxBarsPerRequest: 100, TimestampMode: marketdata.TimestampModeOpen, HasAmount: true,
		Status: marketdata.SourceEnabled, Now: now,
	})
}

func rowFields(row *storagepb.RowFieldUpsert) map[string]*storagepb.TypedValue {
	fields := map[string]*storagepb.TypedValue{}
	for _, field := range row.GetFields() {
		fields[field.GetFieldId()] = field.GetValue()
	}
	return fields
}

// A 股的指数、可转债经 Handler 的同一个入口装成单一来源的流水线（不按路由分配）。
//
// 这两个品种现在采不了，改造前后一样，有两道互不相关的限制，都不是这次改的：
//   - 真实的指数、可转债代码（000300.XSHG、113050.XSHG）过不了 A 股的标的规则——它按股票代码段校验归属的交易所，
//     所以取数项逐项报 symbol 错；生成采集计划时同样因此整组跳过，这类任务不会出现在计划里。
//   - 这两个品种的数据源在目录里都是 catalog_only（没有做过行情验收），路由器不放行。
//
// 前两个子测试固定这两种情况都是逐项的失败：装配成功、不写入、不影响别的请求。后面的子测试换上已放行的同一种数据源实现
// 和一个过得了代码校验的标的，证明单一来源的流水线本身是通的：装配、标的换算、按品种的行、source_id、午休不取数。
func TestComposedHandlerRunsStockIndexAndConvertibleBondOnTheSingleSourcePipeline(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", "stockcn")
	// 2026-11-10 周二 10:00:30（上海时间），5m 刚收盘的一根是 09:55。
	now := time.Date(2026, 11, 10, 2, 0, 30, 0, time.UTC)
	clock := func() time.Time { return now }
	// run 经 Handler 执行一个请求；released 非空时把流水线里连网的数据源换成它，其余都是生产路径。
	run := func(t *testing.T, released marketdata.MarketProvider, req marketfetch.Request) (*model.Response, *capturedStorage, *marketfetch.KlinePipeline) {
		storage := &capturedStorage{}
		var built *marketfetch.KlinePipeline
		h := NewHandler()
		h.Now = clock
		h.NewStorage = func(string) (marketfetch.Storage, error) { return storage, nil }
		h.NewKlinePipeline = func(s marketfetch.Storage, marketID string, instrument marketdata.InstrumentType, frequency, providerID string, limits *marketfetch.FeedLimits) (*marketfetch.KlinePipeline, error) {
			pipeline, err := NewKlinePipeline(s, marketID, instrument, frequency, providerID, limits)
			if err != nil {
				return nil, err
			}
			if released != nil {
				registry := marketdata.NewRegistry()
				require.NoError(t, registry.Register(released))
				router, routerErr := marketdata.NewRouter(registry, 2, nil, nil)
				require.NoError(t, routerErr)
				pipeline.Router = router
			}
			built = pipeline
			return pipeline, nil
		}
		encoded, err := json.Marshal(req)
		require.NoError(t, err)
		var data map[string]interface{}
		require.NoError(t, json.Unmarshal(encoded, &data))
		response, err := h.HandleWithFunctionName(context.Background(), model.CloudFunctionEvent{Action: model.EventActionMarketFetch, Data: data}, "function")
		require.NoError(t, err)
		return response, storage, built
	}
	request := func(batchKind domain.BatchKind, instrument, provider, sourceID, frequency, subject, symbol string, start, end time.Time) marketfetch.Request {
		item := domain.CollectionItem{SubjectID: subject, Symbol: symbol, Provider: provider, SourceID: sourceID, MarketType: instrument,
			DataType: "kline", DatasetID: "ds_" + instrument, Frequency: frequency, BarLimit: 2}
		if !start.IsZero() {
			item.StartTime, item.EndTime = start.Format(time.RFC3339), end.Format(time.RFC3339)
		}
		return marketfetch.Request{
			BatchID: "batch-" + instrument, BatchKind: batchKind, SpaceID: "stockcn", MarketID: "stockcn", InstrumentType: instrument,
			DatasetID: "ds_" + instrument, Frequency: frequency, Provider: provider, SourceID: sourceID, MarketType: instrument, RequestID: "request",
			Items: []domain.CollectionItem{item},
		}
	}
	failure := func(t *testing.T, response *model.Response) string {
		require.False(t, response.Success)
		raw, err := json.Marshal(response.Data)
		require.NoError(t, err)
		return string(raw)
	}

	t.Run("真实的指数、可转债代码过不了标的规则", func(t *testing.T) {
		for _, tc := range []struct{ instrument, provider, sourceID, frequency, subject, symbol string }{
			{"index", "eastmoney", "index_http", "1d", "000300.XSHG", "sh000300"},
			{"convertible_bond", "sina", "convertible_bond_http", "5m", "113050.XSHG", "sh113050"},
		} {
			response, storage, pipeline := run(t, nil, request(domain.BatchKindRealtime, tc.instrument, tc.provider, tc.sourceID, tc.frequency, tc.subject, tc.symbol, time.Time{}, time.Time{}))
			require.Equal(t, marketdata.InstrumentType(tc.instrument), pipeline.InstrumentType)
			require.Equal(t, tc.sourceID, pipeline.SourceID)
			require.False(t, pipeline.AutoBindSource, "不按路由分配")
			require.Contains(t, failure(t, response), "does not belong to XSHG", tc.instrument)
			require.Empty(t, storage.rows)
		}
	})

	t.Run("数据源没有放行", func(t *testing.T) {
		for _, tc := range []struct{ instrument, provider, sourceID, frequency string }{
			{"index", "csindex", "index_http", "1d"},
			{"convertible_bond", "eastmoney", "convertible_bond_http", "5m"},
		} {
			response, storage, _ := run(t, nil, request(domain.BatchKindRealtime, tc.instrument, tc.provider, tc.sourceID, tc.frequency, "000001.XSHE", "sz000001", time.Time{}, time.Time{}))
			require.Contains(t, failure(t, response), "source is not enabled", tc.instrument)
			require.Empty(t, storage.rows)
		}
	})

	t.Run("放行后指数日线补采写入指数行", func(t *testing.T) {
		server := klineServer(t, "2026-11-06", "2026-11-09")
		start, end := time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
		response, storage, _ := run(t, releasedSource(server, "eastmoney", "index_http", marketdata.InstrumentIndex, "1d", clock),
			request(domain.BatchKindBackfill, "index", "eastmoney", "index_http", "1d", "000001.XSHE", "sz000001", start, end))
		require.True(t, response.Success, "%+v", response)
		require.Len(t, storage.rows, 2)
		key := storage.rows[0].GetKey()
		require.Equal(t, "ds_index", key.GetDatasetId())
		require.Equal(t, "000001.XSHE", key.GetTimeSeries().GetSubjectId())
		require.Equal(t, "default", key.GetTimeSeries().GetSeriesTag())
		require.Equal(t, "2026-11-06T00:00:00Z", key.GetTimeSeries().GetDataTime())
		fields := rowFields(storage.rows[0])
		require.Equal(t, "sz000001", fields["index_code"].GetStringValue(), "代码由标的规则换算")
		require.Equal(t, "eastmoney", fields["provider_id"].GetStringValue())
		require.Equal(t, "index_http", fields["source_id"].GetStringValue())
		require.Equal(t, 345600.0, fields["amount"].GetDoubleValue())
		require.NotContains(t, fields, "route_id", "指数行没有路由字段")
		require.NotContains(t, fields, "quote_volume")
		require.NotContains(t, fields, "instrument_name", "不是股票行")
	})

	t.Run("放行后可转债 5m 实时采集写入普通行", func(t *testing.T) {
		server := klineServer(t, "2026-11-10 09:50", "2026-11-10 09:55")
		// 请求里的 source_id 是旧值：以目录登记的为准，对齐后照常写入。
		response, storage, pipeline := run(t, releasedSource(server, "sina", "convertible_bond_http", marketdata.InstrumentConvertibleBond, "5m", clock),
			request(domain.BatchKindRealtime, "convertible_bond", "sina", "stale_http", "5m", "000001.XSHE", "sz000001", time.Time{}, time.Time{}))
		require.True(t, response.Success, "%+v", response)
		require.Equal(t, "convertible_bond_http", pipeline.SourceID)
		require.Len(t, storage.rows, 2)
		last := storage.rows[1]
		require.Equal(t, "2026-11-10T01:55:00Z", last.GetKey().GetTimeSeries().GetDataTime())
		require.Equal(t, "default", last.GetKey().GetTimeSeries().GetSeriesTag())
		fields := rowFields(last)
		require.Equal(t, "sina", fields["provider_id"].GetStringValue())
		require.Equal(t, "convertible_bond_http", fields["source_id"].GetStringValue())
		require.Equal(t, "sz000001", fields["provider_symbol"].GetStringValue())
		require.NotContains(t, fields, "index_code")
		require.NotContains(t, fields, "route_id")
	})

	t.Run("午休时不取数", func(t *testing.T) {
		server := klineServer(t, "2026-11-10 11:25")
		morning := now
		now = time.Date(2026, 11, 10, 4, 30, 0, 0, time.UTC)
		defer func() { now = morning }()
		response, storage, _ := run(t, releasedSource(server, "sina", "convertible_bond_http", marketdata.InstrumentConvertibleBond, "5m", clock),
			request(domain.BatchKindRealtime, "convertible_bond", "sina", "convertible_bond_http", "5m", "000001.XSHE", "sz000001", time.Time{}, time.Time{}))
		require.True(t, response.Success, "%+v", response)
		require.Empty(t, storage.rows)
	})
}
