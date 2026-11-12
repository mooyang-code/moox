package marketwiring

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	hkeastmoney "github.com/mooyang-code/moox/modules/collector/internal/sources/stockhk/eastmoney"
	"github.com/stretchr/testify/require"
)

// 港股股票的 K 线按路由装配：候选链只有东方财富，source_id、限速、单次根数来自路由与数据源配置文件，结算延迟 5 秒。
func TestStockHKKlinePipelineComesFromTheRouteFiles(t *testing.T) {
	// 请求里写别的数据源也不影响：来源按路由分配。
	pipeline, err := NewKlinePipeline(timerHandlerStorage{}, "stockhk", marketdata.InstrumentEquity, "5m", "tencent", nil)
	require.NoError(t, err)
	require.Equal(t, []string{"eastmoney"}, pipeline.CandidateChain)
	require.True(t, pipeline.AutoBindSource)
	require.Empty(t, pipeline.SourceID)
	require.Equal(t, 5*time.Second, pipeline.SettleDelay)
	require.Equal(t, "stockhk", pipeline.SpaceID)
	require.Equal(t, marketdata.InstrumentEquity, pipeline.InstrumentType)
	require.Empty(t, pipeline.Fields, "港股没有附加字段来源")

	provider, err := pipeline.Router.Provider("eastmoney")
	require.NoError(t, err)
	require.Equal(t, "*eastmoney.Provider eastmoney/stockhk_http", fmt.Sprintf("%T %s", provider, provider.Descriptor().SourceKey()))
	require.IsType(t, &hkeastmoney.Provider{}, provider, "是港股的东方财富，不是 A 股的那一个")
	spec, err := pipeline.Router.NewSession().KlineSpec("eastmoney")
	require.NoError(t, err)
	require.Equal(t, []string{"5m"}, spec.Frequencies)
	require.Equal(t, 1205, spec.MaxBarsPerRequest)
	require.Equal(t, marketdata.RateLimitPolicy{RequestsPerSecond: 50, Burst: 20, MaxConcurrent: 16, Cooldown: time.Second, RequestTimeout: 5 * time.Second}, spec.RateLimit)

	// 只有 5m 的路由。
	for _, frequency := range []string{"1m", "1d"} {
		_, err = NewKlinePipeline(timerHandlerStorage{}, "stockhk", marketdata.InstrumentEquity, frequency, "", nil)
		require.ErrorContains(t, err, "stockhk 没有 "+frequency+" 的 K 线路由")
	}
	require.Equal(t, "stockhk_equity_kline_5m", marketfetch.KlineRouteID("stockhk", marketdata.InstrumentEquity, "5m"))
}

// stockHKPipeline 装一条港股 5m 流水线，只把连网的那一段换掉：数据源仍是港股的东方财富，请求交给 handler，时钟固定在 now。
func stockHKPipeline(t *testing.T, storage marketfetch.Storage, now time.Time, handler http.HandlerFunc) *marketfetch.KlinePipeline {
	t.Helper()
	pipeline, err := NewKlinePipeline(storage, "stockhk", marketdata.InstrumentEquity, "5m", "", nil)
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	clock := func() time.Time { return now }
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(hkeastmoney.New(hkeastmoney.Config{BaseURL: server.URL, HTTPClient: server.Client(), Now: clock})))
	router, err := marketdata.NewRouter(registry, 3, nil, nil)
	require.NoError(t, err)
	pipeline.Router, pipeline.Now = router, clock
	return pipeline
}

func hkTime(value string) time.Time {
	parsed, err := time.ParseInLocation("2006-01-02 15:04:05", value, time.FixedZone("HKT", 8*3600))
	if err != nil {
		panic(err)
	}
	return parsed.UTC()
}

// 定时请求（只要目标那一根）：冷门股 00836 在 14:50 这个边界之后新一根迟迟不出现，刚收盘的 14:50 仍是返回的最后一根
// （样例是 2026-11-12 的实际返回，见 sources/stockhk/eastmoney 的测试），按开始时刻 14:45 挑出来写入。
func TestStockHKScheduledRequestWritesTheTargetBar(t *testing.T) {
	const dataset = "dataset_stockhk_equity_kline_5m"
	target := hkTime("2026-11-12 14:45:00")
	body := []byte(`{"data":{"klines":[` +
		`"2026-11-12 14:45,20.420,20.460,20.460,20.400,212000,4330480.000",` +
		`"2026-11-12 14:50,20.460,20.420,20.460,20.420,24000,490280.000"]}}`)
	request := func() marketfetch.Request {
		item := domain.CollectionItem{
			InstanceID: "instance-1", SubjectID: "00836.XHKG", Symbol: "00836", Provider: "eastmoney", MarketType: "equity", DataType: "kline",
			DatasetID: dataset, Frequency: "5m", TargetDataTime: target.Format(time.RFC3339Nano), SeriesHash: "series-hash", ExpectedCount: 1, RequirePeriodCommit: true,
		}
		return marketfetch.Request{
			BatchID: "scheduled", BatchKind: domain.BatchKindRealtime, SpaceID: "stockhk", MarketID: "stockhk", InstrumentType: "equity",
			DatasetID: dataset, Frequency: "5m", MarketType: "equity", RequestID: "scheduled", RequirePeriodCommit: true,
			Items: []domain.CollectionItem{item},
			Targets: []domain.WriteTarget{{
				ID: "target-1", SpaceID: "stockhk", InstanceID: item.InstanceID, TaskID: "task-1", DatasetID: dataset,
				SeriesHash: item.SeriesHash, ExpectedCount: 1, Frequency: "5m", TargetDataTime: item.TargetDataTime,
			}},
		}
	}

	storage := &capturedStorage{}
	var query string
	pipeline := stockHKPipeline(t, storage, hkTime("2026-11-12 14:50:05"), func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		_, _ = w.Write(body)
	})
	payload, err := pipeline.Execute(context.Background(), request())
	require.NoError(t, err)
	require.Equal(t, "succeeded", payload.GetStatus(), payload.GetErrorSummary())
	require.Contains(t, query, "secid=116.00836&klt=5&fqt=0&beg=20261112&end=20261112&lmt=1205")
	require.Len(t, storage.committed, 1, "只写目标那一根")
	key := storage.committed[0].GetRow().GetKey()
	require.Equal(t, dataset, key.GetDatasetId())
	require.Equal(t, "00836.XHKG", key.GetTimeSeries().GetSubjectId())
	require.Equal(t, "default", key.GetTimeSeries().GetSeriesTag())
	require.Equal(t, "2026-11-12T06:45:00Z", key.GetTimeSeries().GetDataTime(), "键是开盘时刻 14:45（香港时间）")
	fields := rowFields(storage.committed[0].GetRow())
	require.Equal(t, 20.42, fields["close"].GetDoubleValue())
	require.Equal(t, 24000.0, fields["volume"].GetDoubleValue())
	require.Equal(t, 490280.0, fields["amount"].GetDoubleValue())
	require.Equal(t, "shares", fields["volume_unit"].GetStringValue())
	require.Equal(t, "hkd", fields["amount_unit"].GetStringValue())
	require.Equal(t, "eastmoney", fields["provider_id"].GetStringValue())
	require.Equal(t, "stockhk_http", fields["source_id"].GetStringValue())
	require.Equal(t, "00836", fields["provider_symbol"].GetStringValue())
	require.Equal(t, "stockhk_equity_kline_5m", fields["route_id"].GetStringValue())

	// 没等够结算延迟（收盘后 5 秒）就取：目标那根不写，这一项失败，等下一次。
	early := &capturedStorage{}
	pipeline = stockHKPipeline(t, early, hkTime("2026-11-12 14:50:04"), func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) })
	payload, err = pipeline.Execute(context.Background(), request())
	require.NoError(t, err)
	require.Equal(t, "failed", payload.GetStatus())
	require.Empty(t, early.committed)
}

// 补采请求（给区间）：返回区间内的全部根。样例是 00700 在 2026-11-11 整天的实际返回，66 根。
func TestStockHKBackfillRequestWritesEveryBarInTheRange(t *testing.T) {
	const dataset = "dataset_stockhk_equity_kline_5m"
	body, err := os.ReadFile(filepath.Join("..", "sources", "stockhk", "eastmoney", "testdata", "kline_5m_00700_20261111.json"))
	require.NoError(t, err)
	storage := &capturedStorage{}
	pipeline := stockHKPipeline(t, storage, hkTime("2026-11-12 10:00:00"), func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "116.00700", r.URL.Query().Get("secid"))
		require.Equal(t, "20261111", r.URL.Query().Get("beg"))
		require.Equal(t, "20261111", r.URL.Query().Get("end"))
		_, _ = w.Write(body)
	})
	run := func(start, end string) []string {
		storage.rows = nil
		payload, err := pipeline.Execute(context.Background(), marketfetch.Request{
			BatchID: "backfill", BatchKind: domain.BatchKindBackfill, SpaceID: "stockhk", MarketID: "stockhk", InstrumentType: "equity",
			DatasetID: dataset, Frequency: "5m", MarketType: "equity", RequestID: "backfill",
			Items: []domain.CollectionItem{{
				SubjectID: "00700.XHKG", Symbol: "00700", Provider: "eastmoney", MarketType: "equity", DataType: "kline", DatasetID: dataset, Frequency: "5m",
				StartTime: hkTime(start).Format(time.RFC3339Nano), EndTime: hkTime(end).Format(time.RFC3339Nano), BarLimit: 100,
			}},
		})
		require.NoError(t, err)
		require.Equal(t, "succeeded", payload.GetStatus(), payload.GetErrorSummary())
		times := make([]string, 0, len(storage.rows))
		for _, row := range storage.rows {
			times = append(times, row.GetKey().GetTimeSeries().GetDataTime())
		}
		return times
	}

	day := run("2026-11-11 09:30:00", "2026-11-11 16:00:00")
	require.Len(t, day, 66)
	require.Equal(t, "2026-11-11T01:30:00Z", day[0], "首根 09:30")
	require.Equal(t, "2026-11-11T03:55:00Z", day[29], "上午最后一根 11:55")
	require.Equal(t, "2026-11-11T05:00:00Z", day[30], "下午第一根 13:00")
	require.Equal(t, "2026-11-11T07:55:00Z", day[65], "末根 15:55")
	last := rowFields(storage.rows[65])
	require.Equal(t, 2819000.0, last["volume"].GetDoubleValue(), "16:00 那根是收市竞价并入后的值")
	require.Equal(t, "stockhk_http", last["source_id"].GetStringValue())

	// 区间只有下午的一段：接口仍返回整天，写入时按区间截取。
	require.Equal(t, []string{"2026-11-11T05:00:00Z", "2026-11-11T05:05:00Z", "2026-11-11T05:10:00Z"}, run("2026-11-11 13:00:00", "2026-11-11 13:15:00"))
}
