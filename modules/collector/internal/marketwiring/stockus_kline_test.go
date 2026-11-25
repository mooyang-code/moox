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
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/fetchplan"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	uscnbc "github.com/mooyang-code/moox/modules/collector/internal/sources/stockus/cnbc"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

// 美股股票的 K 线按路由装配：候选链只有 CNBC，source_id、限速来自路由与数据源配置文件，结算延迟 5 秒。
// 假的 CNBC 服务按请求路径里的开始、结束（纽约时间，两端都含）截取 1M 返回：内容是 2026-11-23（冬令时）、2026-10-20（夏令时）
// AAPL 整天的实测返回（截进 sources/stockus/cnbc/testdata），或按交易日合成的 1M；其余都是真实装配：行情源目录、标的规则、路由文件、流水线。

const usDataset = "dataset_stockus_equity_kline_5m"

// usBarsPrefix 是 CNBC 1M 接口的路径前缀，后面接 /{代码}/1M/{开始}/{结束}/unadjusted/EST5EDT.json。
const usBarsPrefix = "/harmony/app/bars/"

var usNewYork = func() *time.Location {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		panic(err)
	}
	return location
}()

// nyTime 把纽约时间（"2006-01-02 15:04"）换成 UTC。
func nyTime(value string) time.Time {
	parsed, err := time.ParseInLocation("2006-01-02 15:04", value, usNewYork)
	if err != nil {
		panic(err)
	}
	return parsed.UTC()
}

// usBarsPath 是取 code 在纽约时间 [from, to]（两端都含，"yyyyMMddHHmm"）内 1M 的请求路径。
func usBarsPath(code, from, to string) string {
	return usBarsPrefix + code + "/1M/" + from + "00/" + to + "00/unadjusted/EST5EDT.json"
}

// usMinute 是假 CNBC 服务返回的一根 1M，字段写法与 CNBC 相同：价格是字符串，量是整数，tradeTime 是纽约时间的开始时刻。
type usMinute struct {
	Open      string `json:"open"`
	High      string `json:"high"`
	Low       string `json:"low"`
	Close     string `json:"close"`
	Volume    int64  `json:"volume"`
	TradeTime string `json:"tradeTime"`
}

// usRecordedMinutes 读 cnbc 包 testdata 里的实测返回。
func usRecordedMinutes(t *testing.T, name string) []usMinute {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "sources", "stockus", "cnbc", "testdata", name))
	require.NoError(t, err)
	var payload struct {
		BarData struct {
			PriceBars []usMinute `json:"priceBars"`
		} `json:"barData"`
	}
	require.NoError(t, json.Unmarshal(raw, &payload))
	require.NotEmpty(t, payload.BarData.PriceBars)
	return payload.BarData.PriceBars
}

// usDay 是一个纽约交易日：收市时刻 16:00，半日市 13:00。
type usDay struct{ date, closeClock string }

// usSessionMinutes 按交易日合成 1M，时间升序：与 CNBC 一样带盘前（09:00 起）和收市后半小时（含收市那一分钟），
// 这些都不属于交易时段，合成 5m 时应当丢掉。内容按序号造：开 100+序号%50，高 开+1，低 开-1，收 开+0.5，量 1000+序号。
func usSessionMinutes(days ...usDay) []usMinute {
	var minutes []usMinute
	for _, day := range days {
		open := nyTime(day.date + " 09:00").In(usNewYork)
		last := nyTime(day.date + " " + day.closeClock).Add(30 * time.Minute).In(usNewYork)
		for at := open; !at.After(last); at = at.Add(time.Minute) {
			index := len(minutes)
			price := 100 + float64(index%50)
			minutes = append(minutes, usMinute{
				Open: fmt.Sprintf("%.4f", price), High: fmt.Sprintf("%.4f", price+1), Low: fmt.Sprintf("%.4f", price-1), Close: fmt.Sprintf("%.4f", price+0.5),
				Volume: int64(1000 + index), TradeTime: at.Format("20060102150405"),
			})
		}
	}
	return minutes
}

// usCNBCServer 是假的 CNBC 1M 接口：按请求路径里的代码取 minutes 给出的 1M，再按开始、结束（纽约时间，两端都含）截取返回，
// 并记下每次请求的路径。minutes 每次请求都调用，可以随时间返回不同的内容。
type usCNBCServer struct {
	t       *testing.T
	minutes func(code string) []usMinute
	mu      sync.Mutex
	paths   []string
}

func newUSCNBCServer(t *testing.T, minutes func(code string) []usMinute) *usCNBCServer {
	return &usCNBCServer{t: t, minutes: minutes}
}

func (s *usCNBCServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.paths = append(s.paths, r.URL.Path)
	s.mu.Unlock()
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, usBarsPrefix), "/")
	if !strings.HasPrefix(r.URL.Path, usBarsPrefix) || len(parts) != 6 || parts[1] != "1M" || parts[4] != "unadjusted" || parts[5] != "EST5EDT.json" {
		http.Error(w, "unexpected path "+r.URL.Path, http.StatusBadRequest)
		return
	}
	if !strings.HasPrefix(r.Header.Get("User-Agent"), "Mozilla/5.0") {
		http.Error(w, "Access Denied", http.StatusForbidden)
		return
	}
	code, from, to := parts[0], parts[2], parts[3]
	picked := []usMinute{}
	for _, minute := range s.minutes(code) {
		if minute.TradeTime >= from && minute.TradeTime <= to {
			picked = append(picked, minute)
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"barData": map[string]any{"symbol": code, "priceBars": picked}})
}

// requested 返回发过的全部请求路径。
func (s *usCNBCServer) requested() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.paths...)
}

// requestsFor 返回某个代码的请求次数。
func (s *usCNBCServer) requestsFor(code string) int {
	count := 0
	for _, path := range s.requested() {
		if strings.HasPrefix(path, usBarsPrefix+code+"/") {
			count++
		}
	}
	return count
}

// usSameMinutes 对任何代码都返回同一组 1M。
func usSameMinutes(minutes []usMinute) func(string) []usMinute {
	return func(string) []usMinute { return minutes }
}

func TestStockUSKlinePipelineComesFromTheRouteFiles(t *testing.T) {
	// 请求里写别的数据源也不影响：来源按路由分配。
	pipeline, err := NewKlinePipeline(timerHandlerStorage{}, "stockus", marketdata.InstrumentEquity, "5m", "sina", nil)
	require.NoError(t, err)
	require.Equal(t, []string{"cnbc"}, pipeline.CandidateChain)
	require.True(t, pipeline.AutoBindSource)
	require.Empty(t, pipeline.SourceID)
	require.Equal(t, 5*time.Second, pipeline.SettleDelay)
	require.Equal(t, "stockus", pipeline.SpaceID)
	require.Equal(t, marketdata.InstrumentEquity, pipeline.InstrumentType)
	require.Empty(t, pipeline.Fields, "美股没有附加字段来源")

	provider, err := pipeline.Router.Provider("cnbc")
	require.NoError(t, err)
	require.IsType(t, &uscnbc.Provider{}, provider)
	require.Equal(t, "cnbc/stockus_cnbc_http", provider.Descriptor().SourceKey().String())
	spec, err := pipeline.Router.NewSession().KlineSpec("cnbc")
	require.NoError(t, err)
	require.Equal(t, []string{"5m"}, spec.Frequencies)
	require.Equal(t, 78, spec.MaxBarsPerRequest)
	require.True(t, spec.History.SupportsArbitraryRange)
	// 数据源配置的超时管整次取数，补采函数用它（历史补齐一块是一次约 20 天的请求）；
	// 实时采集的定时函数按计划的 fetch.request_timeout_ms 设进程环境覆盖它，仍是 3 秒。
	require.Equal(t, marketdata.RateLimitPolicy{RequestsPerSecond: 50, Burst: 20, MaxConcurrent: 16, Cooldown: time.Second, RequestTimeout: 15 * time.Second}, spec.RateLimit)
	require.Equal(t, 3000, usPlanOptions().Fetch.RequestTimeoutMS, "实时采集的单次超时不变")

	// 东方财富只剩标的同步（交易所对照），不再是美股的 K 线来源。
	_, err = pipeline.Router.Provider("eastmoney")
	require.Error(t, err)

	// 只有 5m 的路由。
	for _, frequency := range []string{"1m", "1d"} {
		_, err = NewKlinePipeline(timerHandlerStorage{}, "stockus", marketdata.InstrumentEquity, frequency, "", nil)
		require.ErrorContains(t, err, "stockus 没有 "+frequency+" 的 K 线路由")
	}
	require.Equal(t, "stockus_equity_kline_5m", marketfetch.KlineRouteID("stockus", marketdata.InstrumentEquity, "5m"))

	// 标的规则换出的代码保留点：BRK.B.XNYS → BRK.B（CNBC 认 BRK.B）。
	symbol, err := ResolveSymbol("cnbc", "stockus", "equity", "BRK.B.XNYS")
	require.NoError(t, err)
	require.Equal(t, "BRK.B", symbol)
}

// stockUSPipeline 装一条美股 5m 流水线，只把连网的那一段换掉：数据源仍是 CNBC（限速与装配出来的相同），
// 熔断阈值沿用装配出来的，请求交给 handler，时钟固定在 now。limits 是请求带的限速，可以为空。
func stockUSPipeline(t *testing.T, storage marketfetch.Storage, now time.Time, limits *marketfetch.FeedLimits, handler http.Handler) *marketfetch.KlinePipeline {
	t.Helper()
	pipeline, err := NewKlinePipeline(storage, "stockus", marketdata.InstrumentEquity, "5m", "", limits)
	require.NoError(t, err)
	spec, err := pipeline.Router.NewSession().KlineSpec("cnbc")
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	clock := func() time.Time { return now }
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(uscnbc.New(uscnbc.Config{
		BaseURL: server.URL, SourceID: uscnbc.DefaultSourceID, HTTPClient: server.Client(), Now: clock, RateLimit: spec.RateLimit,
	})))
	router, err := marketdata.NewRouter(registry, pipeline.Router.BreakerThreshold(), nil, nil)
	require.NoError(t, err)
	pipeline.Router, pipeline.Now = router, clock
	return pipeline
}

// usRealtimeRequest 是云函数定时请求（只要目标那一根）：target 是目标那根的开始时刻（UTC）。
// 美股的来源按路由分配，计划生成的取数项不带 provider、source_id。
func usRealtimeRequest(subject, symbol string, target time.Time) marketfetch.Request {
	item := domain.CollectionItem{
		InstanceID: "instance-1", SubjectID: subject, Symbol: symbol, MarketType: "equity", DataType: "kline",
		DatasetID: usDataset, Frequency: "5m", TargetDataTime: target.Format(time.RFC3339Nano), SeriesHash: "series-hash", ExpectedCount: 1, RequirePeriodCommit: true,
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

// usBackfillRequest 是补采分块：[start, end) 按 K 线开始时刻（UTC）。
func usBackfillRequest(subject, symbol string, start, end time.Time) marketfetch.Request {
	return marketfetch.Request{
		BatchID: "backfill", BatchKind: domain.BatchKindBackfill, SpaceID: "stockus", MarketID: "stockus", InstrumentType: "equity",
		DatasetID: usDataset, Frequency: "5m", MarketType: "equity", RequestID: "backfill",
		Items: []domain.CollectionItem{{
			SubjectID: subject, Symbol: symbol, MarketType: "equity", DataType: "kline", DatasetID: usDataset, Frequency: "5m",
			StartTime: start.Format(time.RFC3339Nano), EndTime: end.Format(time.RFC3339Nano), BarLimit: 1000,
		}},
	}
}

// requireCNBCRow 断言写出的行来自 CNBC：来源、路由、单位，成交额是估算值。
func requireCNBCRow(t *testing.T, fields map[string]*storagepb.TypedValue, symbol string) {
	t.Helper()
	require.Equal(t, "cnbc", fields["provider_id"].GetStringValue())
	require.Equal(t, "cnbc", fields["source_provider"].GetStringValue())
	require.Equal(t, "stockus_cnbc_http", fields["source_id"].GetStringValue())
	require.Equal(t, "estimated", fields["amount_quality"].GetStringValue(), "CNBC 没有成交额，按 成交量 × 均价 估算")
	require.Equal(t, "shares", fields["volume_unit"].GetStringValue())
	require.Equal(t, "usd", fields["amount_unit"].GetStringValue())
	require.Equal(t, symbol, fields["provider_symbol"].GetStringValue())
	require.Equal(t, "stockus_equity_kline_5m", fields["route_id"].GetStringValue())
}

// 定时请求（只要目标那一根）：冬令时（2026-11-23）、夏令时（2026-10-20）的首根（纽约 09:30）与末根（15:55），用整天的实测 1M。
// 断言发给假服务的路径（纽约时间，终点是目标那根的最后一分钟），写入的只有目标那一根，键是开始时刻（UTC），
// 开高低收量由那 5 根 1M 合成；末根不含 16:00 那根 1M（收盘竞价）。
func TestStockUSScheduledRequestWritesTheTargetBar(t *testing.T) {
	for _, tc := range []struct {
		name, file, target, from, to string
		dataTime                     string
		// open, high, low, close, volume
		want   [5]float64
		amount float64
	}{
		{
			name: "冬令时首根", file: "aapl_1m_20261123.json", target: "2026-11-23 09:30", from: "202611230930", to: "202611230934",
			dataTime: "2026-11-23T14:30:00Z", want: [5]float64{347.82, 349.76, 347.01, 347.79, 2147640}, amount: 748029888.33,
		},
		{
			name: "冬令时末根", file: "aapl_1m_20261123.json", target: "2026-11-23 15:55", from: "202611231555", to: "202611231559",
			dataTime: "2026-11-23T20:55:00Z", want: [5]float64{349.66, 349.7, 348.67, 349.1, 2377531}, amount: 830172640.935,
		},
		{
			name: "夏令时首根", file: "aapl_1m_20261020.json", target: "2026-10-20 09:30", from: "202610200930", to: "202610200934",
			dataTime: "2026-10-20T13:30:00Z", want: [5]float64{339.86, 342.66, 338.7, 342.32, 1638703}, amount: 558640276.506725,
		},
		{
			name: "夏令时末根", file: "aapl_1m_20261020.json", target: "2026-10-20 15:55", from: "202610201555", to: "202610201559",
			dataTime: "2026-10-20T19:55:00Z", want: [5]float64{339.49, 339.58, 339.2, 339.47, 816539}, amount: 277126794.29105,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := nyTime(tc.target)
			server := newUSCNBCServer(t, usSameMinutes(usRecordedMinutes(t, tc.file)))
			storage := &capturedStorage{}
			// 时钟在收盘后 5 秒（结算延迟刚好过去）。
			pipeline := stockUSPipeline(t, storage, target.Add(5*time.Minute+5*time.Second), nil, server)
			payload, err := pipeline.Execute(context.Background(), usRealtimeRequest("AAPL.XNAS", "AAPL", target))
			require.NoError(t, err)
			require.Equal(t, "succeeded", payload.GetStatus(), payload.GetErrorSummary())
			require.Equal(t, []string{usBarsPath("AAPL", tc.from, tc.to)}, server.requested())
			require.Len(t, storage.committed, 1, "只写目标那一根")
			key := storage.committed[0].GetRow().GetKey()
			require.Equal(t, usDataset, key.GetDatasetId())
			require.Equal(t, "AAPL.XNAS", key.GetTimeSeries().GetSubjectId())
			require.Equal(t, "default", key.GetTimeSeries().GetSeriesTag())
			require.Equal(t, tc.dataTime, key.GetTimeSeries().GetDataTime(), "键是开始时刻")
			fields := rowFields(storage.committed[0].GetRow())
			require.Equal(t, tc.want, [5]float64{
				fields["open"].GetDoubleValue(), fields["high"].GetDoubleValue(), fields["low"].GetDoubleValue(),
				fields["close"].GetDoubleValue(), fields["volume"].GetDoubleValue(),
			})
			require.InDelta(t, tc.amount, fields["amount"].GetDoubleValue(), 1e-3, "成交额按每根 1M 的 成交量 ×（开 + 高 + 低 + 收）/ 4 求和")
			requireCNBCRow(t, fields, "AAPL")
		})
	}

	// 没等够结算延迟（收盘后 5 秒）就取：目标那根不写，这一项失败，等下一次。
	early := &capturedStorage{}
	target := nyTime("2026-11-23 09:30")
	server := newUSCNBCServer(t, usSameMinutes(usRecordedMinutes(t, "aapl_1m_20261123.json")))
	pipeline := stockUSPipeline(t, early, target.Add(5*time.Minute+4*time.Second), nil, server)
	payload, err := pipeline.Execute(context.Background(), usRealtimeRequest("AAPL.XNAS", "AAPL", target))
	require.NoError(t, err)
	require.Equal(t, "failed", payload.GetStatus())
	require.Empty(t, early.committed)
}

// 目标那一根取不到时（那 5 分钟 CNBC 还没有 1M，或标的停牌没有成交）：返回里没有 priceBars 或只有区间外的根，
// 都是“没有已收盘的 K 线”（ErrNoClosedBar），这一项失败等重试或补采，不写数据，也不是回看覆盖错误。
func TestStockUSScheduledRequestFailsWhenTheTargetBarIsMissing(t *testing.T) {
	for _, tc := range []struct {
		name, target string
		minutes      []usMinute
	}{
		// 前一个交易日整天都有，目标日一根都没有。
		{"冬令时首根", "2026-11-24 09:30", usRecordedMinutes(t, "aapl_1m_20261123.json")},
		{"夏令时首根", "2026-10-21 09:30", usRecordedMinutes(t, "aapl_1m_20261020.json")},
		// 盘中缺了目标那 5 分钟：前后的 1M 都在。
		{"盘中缺目标那 5 分钟", "2026-11-23 10:00", func() []usMinute {
			var kept []usMinute
			for _, minute := range usRecordedMinutes(t, "aapl_1m_20261123.json") {
				if minute.TradeTime < "20261123100000" || minute.TradeTime > "20261123100400" {
					kept = append(kept, minute)
				}
			}
			return kept
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := nyTime(tc.target)
			storage := &capturedStorage{}
			pipeline := stockUSPipeline(t, storage, target.Add(5*time.Minute+5*time.Second), nil, newUSCNBCServer(t, usSameMinutes(tc.minutes)))
			payload, err := pipeline.Execute(context.Background(), usRealtimeRequest("AAPL.XNAS", "AAPL", target))
			require.NoError(t, err)
			require.Equal(t, "failed", payload.GetStatus())
			require.Contains(t, payload.GetErrorSummary(), "no closed bar")
			require.NotContains(t, payload.GetErrorSummary(), "coverage")
			require.Empty(t, storage.committed)
		})
	}
}

// 补采请求（给区间）：只写区间内的根；返回里的盘前、盘后与 16:00 那根 1M 不进任何一根，也不让请求失败。
// 实测的冬令时、夏令时整天与盘中一段；合成的跨周末、跨夏令时结束（2026-11-01）、周一首根单根、超过 5 天分几次请求。
func TestStockUSBackfillWritesOnlyTheRange(t *testing.T) {
	recorded := func(file string) []usMinute { return usRecordedMinutes(t, file) }
	weeks := usSessionMinutes(
		usDay{"2026-10-29", "16:00"}, usDay{"2026-10-30", "16:00"}, // 夏令时最后一周的周四、周五
		usDay{"2026-11-02", "16:00"}, usDay{"2026-11-03", "16:00"}, usDay{"2026-11-04", "16:00"}, usDay{"2026-11-05", "16:00"}, usDay{"2026-11-06", "16:00"},
		usDay{"2026-11-09", "16:00"}, usDay{"2026-11-10", "16:00"},
	)
	for _, tc := range []struct {
		name       string
		minutes    []usMinute
		start, end string
		now        time.Time
		count      int
		first      string
		last       string
		paths      []string
	}{
		{
			name: "冬令时整天（实测）", minutes: recorded("aapl_1m_20261123.json"), start: "2026-11-23 09:30", end: "2026-11-23 16:00", now: nyTime("2026-11-23 20:00"),
			count: 78, first: "2026-11-23T14:30:00Z", last: "2026-11-23T20:55:00Z",
			paths: []string{usBarsPath("AAPL", "202611230930", "202611231559")},
		},
		{
			name: "夏令时整天（实测），区间是纽约零点到次日零点", minutes: recorded("aapl_1m_20261020.json"), start: "2026-10-20 00:00", end: "2026-10-21 00:00", now: nyTime("2026-10-21 08:00"),
			count: 78, first: "2026-10-20T13:30:00Z", last: "2026-10-20T19:55:00Z",
			paths: []string{usBarsPath("AAPL", "202610200000", "202610202359")},
		},
		{
			name: "盘中一段（实测）", minutes: recorded("aapl_1m_20261123.json"), start: "2026-11-23 10:00", end: "2026-11-23 11:00", now: nyTime("2026-11-23 20:00"),
			count: 12, first: "2026-11-23T15:00:00Z", last: "2026-11-23T15:55:00Z",
			paths: []string{usBarsPath("AAPL", "202611231000", "202611231059")},
		},
		{
			name: "跨周末：周五、周一两个冬令时交易日", minutes: weeks, start: "2026-11-06 09:30", end: "2026-11-09 16:00", now: nyTime("2026-11-10 20:00"),
			count: 156, first: "2026-11-06T14:30:00Z", last: "2026-11-09T20:55:00Z",
			paths: []string{usBarsPath("AAPL", "202611060930", "202611091559")},
		},
		{
			name: "跨周末：周一首根", minutes: weeks, start: "2026-11-09 09:30", end: "2026-11-09 09:35", now: nyTime("2026-11-10 20:00"),
			count: 1, first: "2026-11-09T14:30:00Z", last: "2026-11-09T14:30:00Z",
			paths: []string{usBarsPath("AAPL", "202611090930", "202611090934")},
		},
		{
			name: "跨夏令时结束：10-30（夏令时）与 11-02（冬令时），路径按纽约时间写", minutes: weeks, start: "2026-10-30 09:30", end: "2026-11-02 16:00", now: nyTime("2026-11-10 20:00"),
			count: 156, first: "2026-10-30T13:30:00Z", last: "2026-11-02T20:55:00Z",
			paths: []string{usBarsPath("AAPL", "202610300930", "202611021559")},
		},
		{
			// 历史补齐跨交易日合并的分块：限速配置的超时管整次取数，一块一次请求取完，不分窗多跑来回。
			name: "合并分块（13 个自然日、跨夏令时结束）一次请求", minutes: weeks, start: "2026-10-29 00:00", end: "2026-11-11 00:00", now: nyTime("2026-11-11 08:00"),
			count: 9 * 78, first: "2026-10-29T13:30:00Z", last: "2026-11-10T20:55:00Z",
			paths: []string{usBarsPath("AAPL", "202610290000", "202611102359")},
		},
		{
			name: "超过 31 天分几次请求，段与段之间不重复", minutes: weeks, start: "2026-10-01 00:00", end: "2026-11-11 00:00", now: nyTime("2026-11-11 08:00"),
			count: 9 * 78, first: "2026-10-29T13:30:00Z", last: "2026-11-10T20:55:00Z",
			// 每段 31 天，按绝对时长切：10-01 零点（夏令时）加 31 天是 11-01 零点（夏令时在当天 2 点才结束）；
			// 最后一段到区间终点（11-11 零点，冬令时）前一分钟。
			paths: []string{
				usBarsPath("AAPL", "202610010000", "202610312359"),
				usBarsPath("AAPL", "202611010000", "202611102359"),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newUSCNBCServer(t, usSameMinutes(tc.minutes))
			storage := &capturedStorage{}
			pipeline := stockUSPipeline(t, storage, tc.now, nil, server)
			payload, err := pipeline.Execute(context.Background(), usBackfillRequest("AAPL.XNAS", "AAPL", nyTime(tc.start), nyTime(tc.end)))
			require.NoError(t, err)
			require.Equal(t, "succeeded", payload.GetStatus(), payload.GetErrorSummary())
			require.Equal(t, tc.paths, server.requested())
			require.Len(t, storage.rows, tc.count)
			seen := map[string]bool{}
			for _, row := range storage.rows {
				dataTime := row.GetKey().GetTimeSeries().GetDataTime()
				require.False(t, seen[dataTime], "同一根只写一次：%s", dataTime)
				seen[dataTime] = true
				require.Equal(t, "AAPL.XNAS", row.GetKey().GetTimeSeries().GetSubjectId())
				at, err := time.Parse(time.RFC3339, dataTime)
				require.NoError(t, err)
				require.False(t, at.Before(nyTime(tc.start)) || !at.Before(nyTime(tc.end)), "%s 在区间外", dataTime)
				clock := at.In(usNewYork).Format("15:04")
				require.True(t, clock >= "09:30" && clock <= "15:55", "%s 不在交易时段", dataTime)
			}
			require.Equal(t, tc.first, storage.rows[0].GetKey().GetTimeSeries().GetDataTime())
			require.Equal(t, tc.last, storage.rows[len(storage.rows)-1].GetKey().GetTimeSeries().GetDataTime())
			requireCNBCRow(t, rowFields(storage.rows[0]), "AAPL")
		})
	}
}

// 半日市（2026-11-27 感恩节次日，13:00 收市，冬令时）：42 根，09:30～12:55；13:00 那根 1M（收盘竞价）与之后的盘后 1M 丢掉。
// 没有真实样例，用合成的 1M；补采按纽约当天零点到次日零点的区间写入 42 根，不多不少。
func TestStockUSHalfDayWritesFortyTwoBars(t *testing.T) {
	server := newUSCNBCServer(t, usSameMinutes(usSessionMinutes(usDay{"2026-11-27", "13:00"})))
	storage := &capturedStorage{}
	pipeline := stockUSPipeline(t, storage, nyTime("2026-11-27 13:10"), nil, server)
	payload, err := pipeline.Execute(context.Background(), usBackfillRequest("AAPL.XNAS", "AAPL", nyTime("2026-11-27 00:00"), nyTime("2026-11-28 00:00")))
	require.NoError(t, err)
	require.Equal(t, "succeeded", payload.GetStatus(), payload.GetErrorSummary())
	require.Equal(t, []string{usBarsPath("AAPL", "202611270000", "202611271309")}, server.requested(), "终点不超过当前时刻")
	require.Len(t, storage.rows, 42)
	require.Equal(t, "2026-11-27T14:30:00Z", storage.rows[0].GetKey().GetTimeSeries().GetDataTime())
	require.Equal(t, "2026-11-27T17:55:00Z", storage.rows[41].GetKey().GetTimeSeries().GetDataTime(), "12:55 那根是当天最后一根")
}

// 带点的代码与三个交易所：请求路径里的代码就是标的规则换出的代码，带类别的保留点（BRK.B，CNBC 不认 BRK_B），
// 请求不带交易所；写出的行上标的 ID 与传给数据源的代码都保留点。
func TestStockUSRequestsUseExchangeAndDottedCode(t *testing.T) {
	minutes := usSessionMinutes(usDay{"2026-11-12", "16:00"})
	target := nyTime("2026-11-12 09:45")
	for _, tc := range []struct{ subject, symbol string }{
		{"AAPL.XNAS", "AAPL"},
		{"JPM.XNYS", "JPM"},
		{"BRK.B.XNYS", "BRK.B"},
		{"BF.B.XNYS", "BF.B"},
		{"SPY.XASE", "SPY"},
	} {
		t.Run(tc.subject, func(t *testing.T) {
			symbol, err := ResolveSymbol("cnbc", "stockus", "equity", tc.subject)
			require.NoError(t, err)
			require.Equal(t, tc.symbol, symbol, "标的规则换出的代码")
			server := newUSCNBCServer(t, usSameMinutes(minutes))
			storage := &capturedStorage{}
			pipeline := stockUSPipeline(t, storage, target.Add(5*time.Minute+5*time.Second), nil, server)
			payload, err := pipeline.Execute(context.Background(), usRealtimeRequest(tc.subject, tc.symbol, target))
			require.NoError(t, err)
			require.Equal(t, "succeeded", payload.GetStatus(), payload.GetErrorSummary())
			require.Equal(t, []string{usBarsPath(tc.symbol, "202611120945", "202611120949")}, server.requested())
			require.Len(t, storage.committed, 1)
			key := storage.committed[0].GetRow().GetKey().GetTimeSeries()
			require.Equal(t, tc.subject, key.GetSubjectId())
			require.Equal(t, "2026-11-12T14:45:00Z", key.GetDataTime())
			requireCNBCRow(t, rowFields(storage.committed[0].GetRow()), tc.symbol)
		})
	}
}

// 候选链只有一个数据源，熔断阈值（同时是在途请求上限）按单一来源的口径算：每源尝试次数 × 并发，与港股相同。
func TestStockUSSingleSourceBreakerThreshold(t *testing.T) {
	t.Setenv("MOOX_FETCH_HTTP_MAX_ATTEMPTS", "")
	t.Setenv("MOOX_FETCH_FEED_CONCURRENCY", "")
	planned := fetchplan.RouteFetchLimits("stockus")(fetchplan.TaskGroup{Frequency: "5m"}, marketfetch.PlanFetch{})
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

// 标的当天一根都没有（全天停牌，或已退市而标签还没同步掉）：补采分块报“没有已收盘的 K 线”（ErrNoClosedBar），
// 补采把它当成行情源确认没有；不能报回看覆盖错误（那会让整个数据集的自动补采卡在同一个水位）。
// 上游有停牌日前后两天的 1M、停牌日一根都没有：夏令时 10-29、冬令时 11-11 各取首根单根、整天、午后一段的分块。
// 盘中部分停牌（区间内有根、缺开头几根）报覆盖错误是港股、A 股原有的行为，不在这里改。
func TestStockUSSuspendedDayBackfillReportsNoClosedBar(t *testing.T) {
	for _, tc := range []struct {
		name, start, end string
		days             []usDay
	}{
		{"夏令时首根单根分块", "2026-10-29 09:30", "2026-10-29 09:35", []usDay{{"2026-10-28", "16:00"}, {"2026-10-30", "16:00"}}},
		{"夏令时整天分块", "2026-10-29 00:00", "2026-10-30 00:00", []usDay{{"2026-10-28", "16:00"}, {"2026-10-30", "16:00"}}},
		{"夏令时午后一段", "2026-10-29 13:00", "2026-10-29 16:00", []usDay{{"2026-10-28", "16:00"}, {"2026-10-30", "16:00"}}},
		{"冬令时首根单根分块", "2026-11-11 09:30", "2026-11-11 09:35", []usDay{{"2026-11-10", "16:00"}, {"2026-11-12", "16:00"}}},
		{"冬令时整天分块（下一个交易日已有数据）", "2026-11-11 00:00", "2026-11-12 00:00", []usDay{{"2026-11-10", "16:00"}, {"2026-11-12", "16:00"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storage := &capturedStorage{}
			pipeline := stockUSPipeline(t, storage, nyTime("2026-11-13 10:00"), nil, newUSCNBCServer(t, usSameMinutes(usSessionMinutes(tc.days...))))
			payload, err := pipeline.Execute(context.Background(), usBackfillRequest("AAPL.XNAS", "AAPL", nyTime(tc.start), nyTime(tc.end)))
			require.NoError(t, err)
			require.Equal(t, "failed", payload.GetStatus())
			require.Contains(t, payload.GetErrorSummary(), "no closed bar")
			require.NotContains(t, payload.GetErrorSummary(), "coverage")
			require.Empty(t, storage.rows)
		})
	}
}
