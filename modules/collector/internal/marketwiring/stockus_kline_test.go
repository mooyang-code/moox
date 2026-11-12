package marketwiring

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	useastmoney "github.com/mooyang-code/moox/modules/collector/internal/sources/stockus/eastmoney"
	"github.com/stretchr/testify/require"
)

// 美股股票的 K 线按路由装配：候选链只有东方财富，source_id、限速、单次根数来自路由与数据源配置文件，结算延迟 5 秒。
// 假的东方财富服务返回 2026-11-12 实测的样例（docs/计划/美股实测数据，截进 sources/stockus/eastmoney/testdata），
// 其余都是真实装配：行情源目录、标的规则、路由文件、流水线。

const usDataset = "dataset_stockus_equity_kline_5m"

// usTime 把北京时间（东方财富美股 K 线标签用的时区）换成 UTC。
func usTime(value string) time.Time {
	parsed, err := time.ParseInLocation("2006-01-02 15:04:05", value, time.FixedZone("CST", 8*3600))
	if err != nil {
		panic(err)
	}
	return parsed.UTC()
}

func usTestdata(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "sources", "stockus", "eastmoney", "testdata", name))
	require.NoError(t, err)
	return body
}

func TestStockUSKlinePipelineComesFromTheRouteFiles(t *testing.T) {
	// 请求里写别的数据源也不影响：来源按路由分配。
	pipeline, err := NewKlinePipeline(timerHandlerStorage{}, "stockus", marketdata.InstrumentEquity, "5m", "sina", nil)
	require.NoError(t, err)
	require.Equal(t, []string{"eastmoney"}, pipeline.CandidateChain)
	require.True(t, pipeline.AutoBindSource)
	require.Empty(t, pipeline.SourceID)
	require.Equal(t, 5*time.Second, pipeline.SettleDelay)
	require.Equal(t, "stockus", pipeline.SpaceID)
	require.Equal(t, marketdata.InstrumentEquity, pipeline.InstrumentType)
	require.Empty(t, pipeline.Fields, "美股没有附加字段来源")

	provider, err := pipeline.Router.Provider("eastmoney")
	require.NoError(t, err)
	require.Equal(t, "*eastmoney.Provider eastmoney/stockus_http", fmt.Sprintf("%T %s", provider, provider.Descriptor().SourceKey()))
	require.IsType(t, &useastmoney.Provider{}, provider, "是美股的东方财富，不是 A 股、港股的")
	spec, err := pipeline.Router.NewSession().KlineSpec("eastmoney")
	require.NoError(t, err)
	require.Equal(t, []string{"5m"}, spec.Frequencies)
	require.Equal(t, 1205, spec.MaxBarsPerRequest)
	require.Equal(t, marketdata.RateLimitPolicy{RequestsPerSecond: 50, Burst: 20, MaxConcurrent: 16, Cooldown: time.Second, RequestTimeout: 5 * time.Second}, spec.RateLimit)

	// 只有 5m 的路由。
	for _, frequency := range []string{"1m", "1d"} {
		_, err = NewKlinePipeline(timerHandlerStorage{}, "stockus", marketdata.InstrumentEquity, frequency, "", nil)
		require.ErrorContains(t, err, "stockus 没有 "+frequency+" 的 K 线路由")
	}
	require.Equal(t, "stockus_equity_kline_5m", marketfetch.KlineRouteID("stockus", marketdata.InstrumentEquity, "5m"))

	// 标的规则换出的代码（保留点）与交易所传给数据源：BRK.B.XNYS → BRK.B、XNYS。
	symbol, err := ResolveSymbol("eastmoney", "stockus", "equity", "BRK.B.XNYS")
	require.NoError(t, err)
	require.Equal(t, "BRK.B", symbol)
}

// stockUSPipeline 装一条美股 5m 流水线，只把连网的那一段换掉：数据源仍是美股的东方财富（限速、单次根数与装配出来的相同），
// 熔断阈值沿用装配出来的，请求交给 handler，时钟固定在 now。limits 是请求带的限速，可以为空。
func stockUSPipeline(t *testing.T, storage marketfetch.Storage, now time.Time, limits *marketfetch.FeedLimits, handler http.HandlerFunc) *marketfetch.KlinePipeline {
	t.Helper()
	pipeline, err := NewKlinePipeline(storage, "stockus", marketdata.InstrumentEquity, "5m", "", limits)
	require.NoError(t, err)
	spec, err := pipeline.Router.NewSession().KlineSpec("eastmoney")
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	clock := func() time.Time { return now }
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(useastmoney.New(useastmoney.Config{
		BaseURL: server.URL, HTTPClient: server.Client(), Now: clock, RateLimit: spec.RateLimit, MaxBarsPerRequest: spec.MaxBarsPerRequest,
	})))
	router, err := marketdata.NewRouter(registry, pipeline.Router.BreakerThreshold(), nil, nil)
	require.NoError(t, err)
	pipeline.Router, pipeline.Now = router, clock
	return pipeline
}

// usRealtimeRequest 是云函数定时请求（只要目标那一根）：target 是目标那根的开始时刻（北京时间）。
func usRealtimeRequest(subject, symbol, target string) marketfetch.Request {
	start := usTime(target)
	item := domain.CollectionItem{
		InstanceID: "instance-1", SubjectID: subject, Symbol: symbol, Provider: "eastmoney", MarketType: "equity", DataType: "kline",
		DatasetID: usDataset, Frequency: "5m", TargetDataTime: start.Format(time.RFC3339Nano), SeriesHash: "series-hash", ExpectedCount: 1, RequirePeriodCommit: true,
	}
	return marketfetch.Request{
		BatchID: "scheduled", BatchKind: domain.BatchKindRealtime, SpaceID: "stockus", MarketID: "stockus", InstrumentType: "equity",
		DatasetID: usDataset, Frequency: "5m", MarketType: "equity", RequestID: "scheduled", RequirePeriodCommit: true,
		Items: []domain.CollectionItem{item},
		Targets: []domain.WriteTarget{{
			ID: "target-1", SpaceID: "stockus", InstanceID: item.InstanceID, TaskID: "task-1", DatasetID: usDataset,
			SeriesHash: item.SeriesHash, ExpectedCount: 1, Frequency: "5m", TargetDataTime: item.TargetDataTime,
		}},
	}
}

// usBackfillRequest 是补采分块：[start, end) 按 K 线开始时刻（北京时间）。
func usBackfillRequest(subject, symbol, start, end string) marketfetch.Request {
	return marketfetch.Request{
		BatchID: "backfill", BatchKind: domain.BatchKindBackfill, SpaceID: "stockus", MarketID: "stockus", InstrumentType: "equity",
		DatasetID: usDataset, Frequency: "5m", MarketType: "equity", RequestID: "backfill",
		Items: []domain.CollectionItem{{
			SubjectID: subject, Symbol: symbol, Provider: "eastmoney", MarketType: "equity", DataType: "kline", DatasetID: usDataset, Frequency: "5m",
			StartTime: usTime(start).Format(time.RFC3339Nano), EndTime: usTime(end).Format(time.RFC3339Nano), BarLimit: 1000,
		}},
	}
}

// withKlinesPrefix 在返回内容的 klines 数组最前面加几行。
func withKlinesPrefix(body []byte, lines ...string) []byte {
	quoted := make([]string, 0, len(lines))
	for _, line := range lines {
		quoted = append(quoted, `"`+line+`"`)
	}
	return []byte(strings.Replace(string(body), `"klines":[`, `"klines":[`+strings.Join(quoted, ",")+",", 1))
}

// 定时请求（只要目标那一根）：冬令时首根（22:35）与末根（次日 05:00）、夏令时首根（21:35）与末根（04:00），
// 断言发给假服务的 beg、end 与日期区间规则一致，写入的只有目标那一根，键是开盘时刻（UTC）。
// 返回内容是实测的整段（含目标之前、之后的根），实际请求时目标之后的根还不存在；这里按时钟固定在收盘后 5 秒，
// 之后的根不返回，之前的根不属于目标，只写目标那一根。
// 每个用例再跑一遍：返回里带着前一时段的一根不合法的根（构造的：开盘价高于最高价，早于目标，在请求区间之外），仍然成功。
func TestStockUSScheduledRequestWritesTheTargetBar(t *testing.T) {
	for _, tc := range []struct {
		name, file, target, now, beg, end string
		dataTime                          string
		// open, high, low, close, volume, amount
		want [6]float64
	}{
		{
			name: "冬令时首根 22:35", file: "kline_5m_AAPL_20261111_20261113.json", target: "2026-11-11 22:30:00", now: "2026-11-11 22:35:05",
			beg: "20261111", end: "20261112", dataTime: "2026-11-11T14:30:00Z",
			want: [6]float64{349.545, 353.35, 349.325, 352.06, 2331875, 816847880},
		},
		{
			name: "冬令时末根次日 05:00", file: "kline_5m_AAPL_20261111_20261113.json", target: "2026-11-12 04:55:00", now: "2026-11-12 05:00:05",
			beg: "20261111", end: "20261113", dataTime: "2026-11-11T20:55:00Z",
			want: [6]float64{355.405, 355.91, 355.28, 355.63, 10630410, 3765914850},
		},
		{
			name: "夏令时周一首根 21:35（beg 是周日）", file: "kline_5m_AAPL_20261025_20261027.json", target: "2026-10-26 21:30:00", now: "2026-10-26 21:35:05",
			beg: "20261025", end: "20261027", dataTime: "2026-10-26T13:30:00Z",
			want: [6]float64{343.9, 346.215, 342.11, 344.05, 2565332, 881328360},
		},
		{
			name: "夏令时末根 04:00", file: "kline_5m_AAPL_20261028_20261031.json", target: "2026-10-30 03:55:00", now: "2026-10-30 04:00:05",
			beg: "20261029", end: "20261031", dataTime: "2026-10-29T19:55:00Z",
			want: [6]float64{348.92, 349.06, 348.3, 348.91, 16479171, 5749156525},
		},
	} {
		for _, dirty := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/前一时段带不合法根=%v", tc.name, dirty), func(t *testing.T) {
				body := usTestdata(t, tc.file)
				if dirty {
					body = withKlinesPrefix(body, "2026-10-23 04:00,100.000,90.000,95.000,80.000,1000,100000.000")
				}
				storage := &capturedStorage{}
				var query map[string]string
				pipeline := stockUSPipeline(t, storage, usTime(tc.now), nil, func(w http.ResponseWriter, r *http.Request) {
					query = map[string]string{}
					for _, name := range []string{"secid", "klt", "fqt", "beg", "end", "lmt"} {
						query[name] = r.URL.Query().Get(name)
					}
					_, _ = w.Write(body)
				})
				payload, err := pipeline.Execute(context.Background(), usRealtimeRequest("AAPL.XNAS", "AAPL", tc.target))
				require.NoError(t, err)
				require.Equal(t, "succeeded", payload.GetStatus(), payload.GetErrorSummary())
				require.Equal(t, map[string]string{"secid": "105.AAPL", "klt": "5", "fqt": "0", "beg": tc.beg, "end": tc.end, "lmt": "1205"}, query)
				require.Len(t, storage.committed, 1, "只写目标那一根")
				key := storage.committed[0].GetRow().GetKey()
				require.Equal(t, usDataset, key.GetDatasetId())
				require.Equal(t, "AAPL.XNAS", key.GetTimeSeries().GetSubjectId())
				require.Equal(t, "default", key.GetTimeSeries().GetSeriesTag())
				require.Equal(t, tc.dataTime, key.GetTimeSeries().GetDataTime(), "键是开盘时刻")
				fields := rowFields(storage.committed[0].GetRow())
				require.Equal(t, tc.want, [6]float64{
					fields["open"].GetDoubleValue(), fields["high"].GetDoubleValue(), fields["low"].GetDoubleValue(),
					fields["close"].GetDoubleValue(), fields["volume"].GetDoubleValue(), fields["amount"].GetDoubleValue(),
				})
				require.Equal(t, "shares", fields["volume_unit"].GetStringValue())
				require.Equal(t, "usd", fields["amount_unit"].GetStringValue())
				require.Equal(t, "eastmoney", fields["provider_id"].GetStringValue())
				require.Equal(t, "stockus_http", fields["source_id"].GetStringValue())
				require.Equal(t, "AAPL", fields["provider_symbol"].GetStringValue())
				require.Equal(t, "stockus_equity_kline_5m", fields["route_id"].GetStringValue())
			})
		}
	}

	// 没等够结算延迟（收盘后 5 秒）就取：目标那根不写，这一项失败，等下一次。
	early := &capturedStorage{}
	body := usTestdata(t, "kline_5m_AAPL_20261111_20261113.json")
	pipeline := stockUSPipeline(t, early, usTime("2026-11-11 22:35:04"), nil, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) })
	payload, err := pipeline.Execute(context.Background(), usRealtimeRequest("AAPL.XNAS", "AAPL", "2026-11-11 22:30:00"))
	require.NoError(t, err)
	require.Equal(t, "failed", payload.GetStatus())
	require.Empty(t, early.committed)
}

// 目标那一根取不到时（上游晚于结算延迟才出新一根）：冬令时首根返回空、夏令时首根返回里只有 beg 取前一天多带的前一时段的根，
// 两种都是“没有已收盘的 K 线”（ErrNoClosedBar），这一项失败等重试或补采，不写数据。
func TestStockUSScheduledRequestFailsWhenTheTargetBarIsMissing(t *testing.T) {
	winter := &capturedStorage{}
	pipeline := stockUSPipeline(t, winter, usTime("2026-11-11 22:35:05"), nil, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"data":{"klines":[]}}`)) })
	payload, err := pipeline.Execute(context.Background(), usRealtimeRequest("AAPL.XNAS", "AAPL", "2026-11-11 22:30:00"))
	require.NoError(t, err)
	require.Equal(t, "failed", payload.GetStatus())
	require.Contains(t, payload.GetErrorSummary(), "no closed bar")
	require.Empty(t, winter.committed)

	// 夏令时首根取不到：返回的是前一时段 10-28 22:30 到 10-29 04:00 的根。
	var tail []string
	for _, line := range strings.Split(string(usTestdata(t, "kline_5m_AAPL_20261028_20261031.json")), "\n") {
		line = strings.TrimSuffix(strings.TrimSpace(line), ",")
		if strings.HasPrefix(line, `"2026-10-28`) || (strings.HasPrefix(line, `"2026-10-29`) && line[12:17] <= "04:00") {
			tail = append(tail, line)
		}
	}
	require.Len(t, tail, 67)
	summer := &capturedStorage{}
	pipeline = stockUSPipeline(t, summer, usTime("2026-10-29 21:35:05"), nil, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"klines":[` + strings.Join(tail, ",") + `]}}`))
	})
	payload, err = pipeline.Execute(context.Background(), usRealtimeRequest("AAPL.XNAS", "AAPL", "2026-10-29 21:30:00"))
	require.NoError(t, err)
	require.Equal(t, "failed", payload.GetStatus())
	require.Contains(t, payload.GetErrorSummary(), "no closed bar", "不是回看覆盖错误")
	require.NotContains(t, payload.GetErrorSummary(), "coverage")
	require.Empty(t, summer.committed)
}

// usUpstream 是东方财富美股日期区间规则的模型（实测得到，见 sources/stockus/eastmoney/daterange.go）：返回收盘标签（北京时间）
// 不早于 beg 日 22:30、在 end 日某个时刻之前的根。cutoff 是那个时刻（05:00 含，或 21:30 不含两个极端）；
// 根的内容是合成的（开高低收量额按序号造，合法），标签取自同日 AAPL 全量返回的 1412 根。
func usUpstream(t *testing.T, cutoff time.Duration, inclusive bool) http.HandlerFunc {
	return usUpstreamDropping(t, cutoff, inclusive, nil)
}

// usUpstreamDropping 同 usUpstream，drop 返回真的标签上游没有这一根（标的停牌、退市）。
func usUpstreamDropping(t *testing.T, cutoff time.Duration, inclusive bool, drop func(label time.Time) bool) http.HandlerFunc {
	t.Helper()
	var recorded struct {
		Universe []string `json:"universe"`
	}
	require.NoError(t, json.Unmarshal(usTestdata(t, "date_range_requests.json"), &recorded))
	beijing := time.FixedZone("CST", 8*3600)
	return func(w http.ResponseWriter, r *http.Request) {
		beg, err := time.ParseInLocation("20060102", r.URL.Query().Get("beg"), beijing)
		require.NoError(t, err)
		end, err := time.ParseInLocation("20060102", r.URL.Query().Get("end"), beijing)
		require.NoError(t, err)
		from, to := beg.Add(22*time.Hour+30*time.Minute), end.Add(cutoff)
		lines := []string{}
		for index, label := range recorded.Universe {
			at, err := time.ParseInLocation("2006-01-02 15:04", label, beijing)
			require.NoError(t, err)
			if at.Before(from) || at.After(to) || (!inclusive && at.Equal(to)) || (drop != nil && drop(at)) {
				continue
			}
			price := 100 + float64(index%50)
			lines = append(lines, fmt.Sprintf(`"%s,%.3f,%.3f,%.3f,%.3f,%d,%d.000"`, label, price, price+0.5, price+1, price-1, 1000+index, (1000+index)*100))
		}
		_, _ = w.Write([]byte(`{"data":{"klines":[` + strings.Join(lines, ",") + `]}}`))
	}
}

// 补采请求（给区间）：只写区间内的根，上游多带的前后的根不写，也不让请求失败。
// 样例里有：实测的冬令时整天（返回还多带 11-12 晚上的 7 根）、实测的夏令时整天（beg 取前一天才带出开盘后 11 根，返回还多带前后各一天）；
// 上游模型（合成内容）下的夏令时周一、跨周末、跨夏令时结束（2026-11-01）的分块，两种切点各跑一遍。
func TestStockUSBackfillWritesOnlyTheRange(t *testing.T) {
	type sample struct {
		name, start, end string
		count            int
		first, last      string
		beg, end2        string
	}
	run := func(t *testing.T, handler http.HandlerFunc, start, end string) ([]string, map[string]string) {
		storage := &capturedStorage{}
		var query map[string]string
		pipeline := stockUSPipeline(t, storage, usTime("2026-11-12 12:00:00"), nil, func(w http.ResponseWriter, r *http.Request) {
			query = map[string]string{"secid": r.URL.Query().Get("secid"), "beg": r.URL.Query().Get("beg"), "end": r.URL.Query().Get("end")}
			handler(w, r)
		})
		payload, err := pipeline.Execute(context.Background(), usBackfillRequest("AAPL.XNAS", "AAPL", start, end))
		require.NoError(t, err)
		require.Equal(t, "succeeded", payload.GetStatus(), payload.GetErrorSummary())
		times := make([]string, 0, len(storage.rows))
		for _, row := range storage.rows {
			times = append(times, row.GetKey().GetTimeSeries().GetDataTime())
			require.Equal(t, "AAPL.XNAS", row.GetKey().GetTimeSeries().GetSubjectId())
		}
		return times, query
	}
	recorded := func(file string) http.HandlerFunc {
		body := usTestdata(t, file)
		return func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }
	}

	t.Run("冬令时整天（实测）", func(t *testing.T) {
		times, query := run(t, recorded("kline_5m_AAPL_20261111_20261113.json"), "2026-11-11 22:30:00", "2026-11-12 05:00:00")
		require.Equal(t, map[string]string{"secid": "105.AAPL", "beg": "20261111", "end": "20261113"}, query)
		require.Len(t, times, 78, "只写 11-11 的 78 根，11-12 晚上多带的 7 根不写")
		require.Equal(t, "2026-11-11T14:30:00Z", times[0])
		require.Equal(t, "2026-11-11T20:55:00Z", times[77])
	})
	t.Run("夏令时整天（实测）：开盘后 11 根与前后多带的根", func(t *testing.T) {
		times, query := run(t, recorded("kline_5m_AAPL_20261028_20261031.json"), "2026-10-29 21:30:00", "2026-10-30 04:00:00")
		require.Equal(t, map[string]string{"secid": "105.AAPL", "beg": "20261028", "end": "20261031"}, query)
		require.Len(t, times, 78, "223 根里只写 10-29 的 78 根")
		require.Equal(t, "2026-10-29T13:30:00Z", times[0], "21:35 标签的首根")
		require.Equal(t, "2026-10-29T19:55:00Z", times[77])
	})
	for _, cutoff := range []struct {
		name      string
		offset    time.Duration
		inclusive bool
	}{{"切点 05:00 含", 5 * time.Hour, true}, {"切点 21:30 不含", 21*time.Hour + 30*time.Minute, false}} {
		for _, tc := range []sample{
			{name: "夏令时周一整天", start: "2026-10-26 21:30:00", end: "2026-10-27 04:00:00", count: 78, first: "2026-10-26T13:30:00Z", last: "2026-10-26T19:55:00Z", beg: "20261025", end2: "20261028"},
			{name: "跨周末：周五、周一两个冬令时交易日", start: "2026-11-06 22:30:00", end: "2026-11-10 05:00:00", count: 156, first: "2026-11-06T14:30:00Z", last: "2026-11-09T20:55:00Z", beg: "20261106", end2: "20261111"},
			{name: "跨周末：周一首根", start: "2026-11-09 22:30:00", end: "2026-11-09 22:35:00", count: 1, first: "2026-11-09T14:30:00Z", last: "2026-11-09T14:30:00Z", beg: "20261109", end2: "20261110"},
			{name: "跨夏令时结束：10-30（夏令时）与 11-02（冬令时）", start: "2026-10-30 21:30:00", end: "2026-11-03 05:00:00", count: 156, first: "2026-10-30T13:30:00Z", last: "2026-11-02T20:55:00Z", beg: "20261029", end2: "20261104"},
			{name: "夏令时结束后的周一首根", start: "2026-11-02 22:30:00", end: "2026-11-02 22:35:00", count: 1, first: "2026-11-02T14:30:00Z", last: "2026-11-02T14:30:00Z", beg: "20261102", end2: "20261103"},
		} {
			t.Run(cutoff.name+"/"+tc.name, func(t *testing.T) {
				times, query := run(t, usUpstream(t, cutoff.offset, cutoff.inclusive), tc.start, tc.end)
				require.Equal(t, map[string]string{"secid": "105.AAPL", "beg": tc.beg, "end": tc.end2}, query)
				require.Len(t, times, tc.count)
				require.Equal(t, tc.first, times[0])
				require.Equal(t, tc.last, times[len(times)-1])
			})
		}
	}
}

// 半日市（2026-11-27 感恩节次日，13:00 收市，冬令时）：42 根，标签 11-27 22:35～11-28 02:00。没有真实样例（计划 U0 第 3 项要等当天实测），
// 用构造的 42 根；补采按区间写入 42 根，不多不少。
func TestStockUSHalfDayWritesFortyTwoBars(t *testing.T) {
	lines := make([]string, 0, 42)
	for label := usTime("2026-11-27 22:35:00"); !label.After(usTime("2026-11-28 02:00:00")); label = label.Add(5 * time.Minute) {
		lines = append(lines, fmt.Sprintf(`"%s,300.000,300.200,300.400,299.800,%d,30000000.000"`, label.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04"), 100000+len(lines)))
	}
	require.Len(t, lines, 42)
	storage := &capturedStorage{}
	pipeline := stockUSPipeline(t, storage, usTime("2026-11-28 02:10:00"), nil, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "20261127", r.URL.Query().Get("beg"))
		require.Equal(t, "20261129", r.URL.Query().Get("end"))
		_, _ = w.Write([]byte(`{"data":{"klines":[` + strings.Join(lines, ",") + `]}}`))
	})
	payload, err := pipeline.Execute(context.Background(), usBackfillRequest("AAPL.XNAS", "AAPL", "2026-11-27 22:30:00", "2026-11-28 02:00:00"))
	require.NoError(t, err)
	require.Equal(t, "succeeded", payload.GetStatus(), payload.GetErrorSummary())
	require.Len(t, storage.rows, 42)
	require.Equal(t, "2026-11-27T14:30:00Z", storage.rows[0].GetKey().GetTimeSeries().GetDataTime())
	require.Equal(t, "2026-11-27T17:55:00Z", storage.rows[41].GetKey().GetTimeSeries().GetDataTime(), "02:00 标签的那根是当天最后一根")
}

// 带点的代码与三个交易所：请求里的 secid 由请求的交易所与代码换出（XNYS 的 BRK.B → 106.BRK_B，XASE 的 SPY → 107.SPY），
// 写出的行上标的 ID 与传给数据源的代码都保留点。
func TestStockUSRequestsUseExchangeAndDottedCode(t *testing.T) {
	body := usTestdata(t, "kline_5m_AAPL_boundary_2250.json")
	for _, tc := range []struct{ subject, symbol, secid string }{
		{"AAPL.XNAS", "AAPL", "105.AAPL"},
		{"JPM.XNYS", "JPM", "106.JPM"},
		{"BRK.B.XNYS", "BRK.B", "106.BRK_B"},
		{"BF.B.XNYS", "BF.B", "106.BF_B"},
		{"SPY.XASE", "SPY", "107.SPY"},
	} {
		storage := &capturedStorage{}
		var secid string
		pipeline := stockUSPipeline(t, storage, usTime("2026-11-12 22:50:05"), nil, func(w http.ResponseWriter, r *http.Request) {
			secid = r.URL.Query().Get("secid")
			_, _ = w.Write(body)
		})
		payload, err := pipeline.Execute(context.Background(), usRealtimeRequest(tc.subject, tc.symbol, "2026-11-12 22:45:00"))
		require.NoError(t, err)
		require.Equal(t, "succeeded", payload.GetStatus(), payload.GetErrorSummary())
		require.Equal(t, tc.secid, secid, tc.subject)
		require.Len(t, storage.committed, 1)
		key := storage.committed[0].GetRow().GetKey().GetTimeSeries()
		require.Equal(t, tc.subject, key.GetSubjectId())
		require.Equal(t, tc.symbol, rowFields(storage.committed[0].GetRow())["provider_symbol"].GetStringValue())
		require.Equal(t, "2026-11-12T14:45:00Z", key.GetDataTime())
	}
}

// 候选链只有一个数据源，熔断阈值（同时是在途请求上限）按单一来源的口径算：每源尝试次数 × 并发，与港股相同。
func TestStockUSSingleSourceBreakerThreshold(t *testing.T) {
	t.Setenv("MOOX_FETCH_HTTP_MAX_ATTEMPTS", "")
	t.Setenv("MOOX_FETCH_FEED_CONCURRENCY", "")
	planned := marketfetch.RouteFetchLimits("stockus")(marketfetch.TaskGroup{Frequency: "5m"}, marketfetch.PlanFetch{})
	require.Equal(t, 16, planned.FeedConcurrency, "采集计划给美股的源并发")
	require.Equal(t, 16, planned.Concurrency, "一个函数内同时处理的标的数")
	threshold := func(limits *marketfetch.FeedLimits) int {
		pipeline, err := NewKlinePipeline(timerHandlerStorage{}, "stockus", marketdata.InstrumentEquity, "5m", "", limits)
		require.NoError(t, err)
		return pipeline.Router.BreakerThreshold()
	}
	require.Equal(t, 48, threshold(&marketfetch.FeedLimits{FeedConcurrency: planned.FeedConcurrency}), "3 × 16")
	require.Equal(t, 3, threshold(nil), "请求没带限速、进程环境也没设并发时按 1")
	t.Setenv("MOOX_FETCH_FEED_CONCURRENCY", "16")
	require.Equal(t, 48, threshold(nil), "云函数里并发来自计划下发的环境变量")
}

// 夏令时前 11 根里最后一根（标签 22:25）和后面的 22:30、冬令时首根 22:35，各自单独作为定时请求的目标：上游模型下 beg 少算一天就取不到
// （beg 的偏移从 22:30 改成 22:25 时，22:25 这一根的 beg 会落在当天、返回里没有它）。实测样例的返回是整段，盖不住这一点，所以用上游模型。
func TestStockUSScheduledRequestForTheFirstBarsOfADaylightSavingDay(t *testing.T) {
	for _, cutoff := range []struct {
		name      string
		offset    time.Duration
		inclusive bool
	}{{"切点 05:00 含", 5 * time.Hour, true}, {"切点 21:30 不含", 21*time.Hour + 30*time.Minute, false}} {
		for _, label := range []string{
			"2026-10-29 21:35", "2026-10-29 21:40", "2026-10-29 22:20", "2026-10-29 22:25", "2026-10-29 22:30", "2026-10-29 22:35", // 夏令时：前 11 根是 21:35～22:25
			"2026-10-26 22:25",                     // 夏令时周一，beg 是周日
			"2026-11-11 22:35", "2026-11-11 22:40", // 冬令时首根
		} {
			t.Run(cutoff.name+"/"+label, func(t *testing.T) {
				closeAt := usTime(label + ":00")
				storage := &capturedStorage{}
				pipeline := stockUSPipeline(t, storage, closeAt.Add(5*time.Second), nil, usUpstream(t, cutoff.offset, cutoff.inclusive))
				target := closeAt.Add(-5 * time.Minute).In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05")
				payload, err := pipeline.Execute(context.Background(), usRealtimeRequest("AAPL.XNAS", "AAPL", target))
				require.NoError(t, err)
				require.Equal(t, "succeeded", payload.GetStatus(), payload.GetErrorSummary())
				require.Len(t, storage.committed, 1)
				require.Equal(t, closeAt.Add(-5*time.Minute).Format(time.RFC3339), storage.committed[0].GetRow().GetKey().GetTimeSeries().GetDataTime())
			})
		}
	}
}

// 标的当天一根都没有（全天停牌，或已退市而标签还没同步掉）：补采分块报“没有已收盘的 K 线”（ErrNoClosedBar），
// 补采把它当成行情源确认没有；不能报回看覆盖错误（那会让整个数据集的自动补采卡在同一个水位）。
// 用上游模型去掉某个交易日的全部根：返回里只剩 beg、end 规则多带回的前后一天的根（或空）。
// 夏令时 10-29（标签 10-29 21:35～10-30 04:00）和冬令时 11-11（标签 11-11 22:35～11-12 05:00）各取首根单根、整天、其他分块。
// 盘中部分停牌（区间内有根、缺开头几根）报覆盖错误是港股、A 股原有的行为，不在这里改。
func TestStockUSSuspendedDayBackfillReportsNoClosedBar(t *testing.T) {
	cst := time.FixedZone("CST", 8*3600)
	dropDay := func(from, to string) func(time.Time) bool {
		start, end := usTime(from+":00"), usTime(to+":00")
		return func(label time.Time) bool { return !label.Before(start) && !label.After(end) }
	}
	for _, tc := range []struct {
		name, start, end string
		drop             func(time.Time) bool
	}{
		{"夏令时首根单根分块", "2026-10-29 21:30:00", "2026-10-29 21:35:00", dropDay("2026-10-29 21:35", "2026-10-30 04:00")},
		{"夏令时 22:30 那一根", "2026-10-29 22:25:00", "2026-10-29 22:30:00", dropDay("2026-10-29 21:35", "2026-10-30 04:00")},
		{"夏令时整天分块", "2026-10-29 21:30:00", "2026-10-30 04:00:00", dropDay("2026-10-29 21:35", "2026-10-30 04:00")},
		{"夏令时凌晨段分块", "2026-10-30 00:00:00", "2026-10-30 04:00:00", dropDay("2026-10-29 21:35", "2026-10-30 04:00")},
		{"冬令时首根单根分块", "2026-11-11 22:30:00", "2026-11-11 22:35:00", dropDay("2026-11-11 22:35", "2026-11-12 05:00")},
		{"冬令时整天分块", "2026-11-11 22:30:00", "2026-11-12 05:00:00", dropDay("2026-11-11 22:35", "2026-11-12 05:00")},
		{"冬令时凌晨段分块（下一时段 11-12 晚上已有数据）", "2026-11-12 00:00:00", "2026-11-12 05:00:00", dropDay("2026-11-11 22:35", "2026-11-12 05:00")},
	} {
		for _, cutoff := range []struct {
			name      string
			offset    time.Duration
			inclusive bool
		}{{"切点 05:00 含", 5 * time.Hour, true}, {"切点 21:30 不含", 21*time.Hour + 30*time.Minute, false}} {
			t.Run(tc.name+"/"+cutoff.name, func(t *testing.T) {
				storage := &capturedStorage{}
				pipeline := stockUSPipeline(t, storage, usTime("2026-11-12 23:00:00").In(cst), nil, usUpstreamDropping(t, cutoff.offset, cutoff.inclusive, tc.drop))
				payload, err := pipeline.Execute(context.Background(), usBackfillRequest("AAPL.XNAS", "AAPL", tc.start, tc.end))
				require.NoError(t, err)
				require.Equal(t, "failed", payload.GetStatus())
				require.Contains(t, payload.GetErrorSummary(), "no closed bar")
				require.NotContains(t, payload.GetErrorSummary(), "coverage")
				require.Empty(t, storage.rows)
			})
		}
	}
}
