// Package cnbc 是美股唯一的 K 线数据源：取 CNBC 网页自用的 1M K 线接口，在本地合成 5m。
//
// 为什么取 1M：CNBC 的 5M 接口滞后 2～3 分钟，1M 收完几秒就是终值；用 1M 合成的 5m 与 CNBC 最终的 5M 完全一致
// （2026-11-23 标普 500 共 503 只全部一致，见 docs/计划/美股盘前数据源调研.md 第四轮）。
//
// 已知差别：
//   - 成交量约为全市场的八成（逐只、逐日差别很大，2026-11-17～11-23 十只常规时段合计是新浪的 0.58～1.0 倍），
//     推断是不计碎股和部分成交条件，没有文档；
//   - 收盘竞价不在任何一根里：CNBC 把它放在开始时刻 16:00 的那根 1M（不在交易时段，丢掉），而且 16:00 及之后的
//     1M 成交量不可信（2026-11-23 NVDA 16:00 那根 32.7 亿股，全天日线才 7.8 亿），所以不拿来补；收盘那根（行键 15:55）
//     的收盘价是 15:59 那分钟的最后一笔，与正式收盘价最多差约 0.5%，量也不含收盘竞价；
//   - 没有成交额，按每根 1M 的 成交量 ×（开 + 高 + 低 + 收）/ 4 求和估算（amount_quality=estimated）；
//     与新浪 5m 的成交额比，全天均价差中位数约 −0.1%，最差 −2.7%；
//   - 网页自用接口，没有文档，随时可能变；不带浏览器 User-Agent 会被 Akamai 返回 403。
package cnbc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/mooyang-code/moox/packages/barclock"
)

const (
	providerID = "cnbc"
	// DefaultSourceID 是行情源目录里登记的 source_id，也是路由文件里写的那一个。
	DefaultSourceID = "stockus_cnbc_http"

	marketID       = "stockus"
	exchangeNasdaq = "XNAS"
	exchangeNYSE   = "XNYS"
	exchangeOther  = "XASE"

	// Host 是 K 线接口的主机。
	Host        = "ts-api.cnbc.com"
	defaultBase = "https://" + Host
	// barsPath 后面接 /{代码}/1M/{开始}/{结束}/unadjusted/EST5EDT.json，开始、结束是纽约时间 yyyyMMddHHmmss，两端都含。
	// 用 unadjusted：与 adjusted 实测返回相同（2026-11-24 核对 KO、AAPL 一年前的日线，含分红日），写明不复权免得口径随上游变。
	barsPath = "/harmony/app/bars"
	// userAgent 必须是浏览器的写法，否则 Akamai 返回 403 Access Denied（2026-11-23 实测）。
	userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36"

	// maxLookback 是 1M 的回看能力：2026-11-24 实测 2025-12-24 仍有、2025-11-24 已没有，按自然时长留余量声明 300 天。
	maxLookback = 300 * 24 * time.Hour
	// maxWindow 是一次请求覆盖的最长时长：多天区间会带上盘前盘后（每天约 960 根 1M），超过就分几次请求。
	maxWindow = 5 * 24 * time.Hour
	// latestWindow 是不带区间时（取最近若干根）回看的时长，覆盖周末加一个休市日。
	latestWindow = 5 * 24 * time.Hour
	// maxBarsPerRequest 是一次请求最多合成的 5m 根数：补采的分块最多是一只标的的一天（78 根）。
	maxBarsPerRequest = 78
	// httpTimeout 是 HTTP 客户端的超时上限；单次请求实际的超时是限速配置里的 request_timeout，随请求的 ctx 生效。
	httpTimeout = 30 * time.Second
	// maxBody 是单次响应的读取上限：5 天的 1M 约 2 万行、约 3MB。
	maxBody = 8 << 20
)

var klineFrequency = marketdata.Frequency5Min

// newYork 是 CNBC 时间标签（tradeTime）的时区：请求里写的 EST5EDT 与纽约时间规则相同。
var newYork = func() *time.Location {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		panic(err)
	}
	return location
}()

// Config 里的地址、source_id、限速来自路由文件与数据源配置文件。
type Config struct {
	BaseURL    string
	SourceID   string
	HTTPClient *http.Client
	Now        func() time.Time
	RateLimit  marketdata.RateLimitPolicy
}

type Provider struct {
	baseURL   string
	sourceID  string
	client    *http.Client
	now       func() time.Time
	rateLimit marketdata.RateLimitPolicy
}

func New(cfg Config) *Provider {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = defaultBase
	}
	if strings.TrimSpace(cfg.SourceID) == "" {
		cfg.SourceID = DefaultSourceID
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.RateLimit.RequestsPerSecond <= 0 {
		cfg.RateLimit = marketdata.RateLimitPolicy{RequestsPerSecond: 50, Burst: 20, MaxConcurrent: 16, Cooldown: time.Second, RequestTimeout: 3 * time.Second}
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: httpTimeout}
	}
	return &Provider{baseURL: strings.TrimRight(cfg.BaseURL, "/"), sourceID: strings.ToLower(strings.TrimSpace(cfg.SourceID)), client: cfg.HTTPClient, now: cfg.Now, rateLimit: cfg.RateLimit}
}

func (p *Provider) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{ID: providerID, SourceID: p.sourceID, DisplayName: "CNBC US", Hosts: []string{Host}, ProtocolVariant: "http", Transport: "https", Port: 443}
}

func (p *Provider) KlineSpec() marketdata.KlineSpec {
	return marketdata.KlineSpec{
		Markets: []string{marketID},
		// 请求不带交易所，但请求里有交易所，这里不声明会被判成不支持。
		Exchanges:         []string{exchangeNasdaq, exchangeNYSE, exchangeOther},
		Frequencies:       []string{string(klineFrequency)},
		CompleteOHLCV:     true,
		HasAmount:         true,
		MaxBarsPerRequest: maxBarsPerRequest,
		TimestampMode:     marketdata.TimestampModeOpen,
		// 按精确区间取：区间开头那根没有成交时，返回里本来就没有它，不能按“返回里要有不晚于起点的根”判覆盖；
		// 超出 1M 能取到的范围由 FetchKlines 自己报 ErrHistoryOutOfRange。
		History:   marketdata.KlineHistoryCapability{SupportsArbitraryRange: true},
		RateLimit: p.rateLimit,
	}
}

// minuteBar 是 CNBC 的一根 1M。
type minuteBar struct {
	Start                  time.Time
	Open, High, Low, Close float64
	Volume                 float64
}

// FetchKlines 取一段 1M 并合成 5m，只返回常规时段内已经收完的根。
//
// 带区间时取 [StartTime, EndTime)（按开始时刻）；不带区间时取最近 Limit 根。区间里一根都没有（全天停牌、已退市、
// 那几分钟没有成交）时报 ErrNoClosedBar，补采会把它当成“行情源确认没有”。
func (p *Provider) FetchKlines(ctx context.Context, req marketdata.KlineRequest) ([]marketdata.NormalizedKline, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	frequency, _ := req.FrequencyValue() // Validate 已校验
	if frequency != klineFrequency {
		return nil, fmt.Errorf("%w: CNBC 美股只合成 5m，不支持 %s", marketdata.ErrUnsupportedFrequency, req.Frequency)
	}
	exchange := strings.ToUpper(strings.TrimSpace(string(req.ExchangeID)))
	if exchange != "" && !slices.Contains([]string{exchangeNasdaq, exchangeNYSE, exchangeOther}, exchange) {
		return nil, fmt.Errorf("%w: 美股标的 %q 的交易所 %q 必须是 %s、%s、%s 之一", marketdata.ErrUnsupportedSymbol, req.SubjectID, req.ExchangeID, exchangeNasdaq, exchangeNYSE, exchangeOther)
	}
	symbol := strings.ToUpper(strings.TrimSpace(req.ProviderSymbol))
	if !validSymbol(symbol) {
		return nil, fmt.Errorf("%w: 美股代码 %q 不合法", marketdata.ErrUnsupportedSymbol, req.ProviderSymbol)
	}
	now := p.now().UTC()
	clock, err := markets.Clock(marketID, string(frequency))
	if err != nil {
		return nil, err
	}
	rangeRequest := !req.StartTime.IsZero() || !req.EndTime.IsZero()
	start, end := req.StartTime.UTC(), req.EndTime.UTC()
	if end.IsZero() || end.After(now) {
		// 取整到分钟：请求里的时刻只写到分钟（秒是 00），这一分钟还没收完的 1M 本来也不会返回。
		end = now.Truncate(time.Minute)
	}
	if start.IsZero() {
		start = end.Add(-latestWindow)
	}
	if start.Before(now.Add(-maxLookback)) {
		return nil, fmt.Errorf("%w: 起点 %s 早于 CNBC 1M 能取到的范围（约 %s）", marketdata.ErrHistoryOutOfRange, start.Format(time.RFC3339), maxLookback)
	}
	var minutes []minuteBar
	for windowStart := start; windowStart.Before(end); windowStart = windowStart.Add(maxWindow) {
		windowEnd := windowStart.Add(maxWindow)
		if windowEnd.After(end) {
			windowEnd = end
		}
		got, err := p.fetchMinutes(ctx, symbol, windowStart, windowEnd)
		if err != nil {
			return nil, err
		}
		minutes = append(minutes, got...)
	}
	bars, err := aggregate(clock, minutes, now)
	if err != nil {
		return nil, err
	}
	rows := make([]marketdata.NormalizedKline, 0, len(bars))
	for _, bar := range bars {
		if rangeRequest && (bar.Start.Before(req.StartTime) || (!req.EndTime.IsZero() && !bar.Start.Before(req.EndTime))) {
			continue
		}
		row := marketdata.NormalizedKline{
			SubjectID: req.SubjectID, ProviderID: providerID, SourceID: p.sourceID, ProviderSymbol: symbol, Frequency: string(frequency),
			BarStart: bar.Start, BarEnd: bar.End,
			Open: bar.Open, High: bar.High, Low: bar.Low, Close: bar.Close, Volume: bar.Volume, Amount: bar.Amount, AmountEstimated: true,
			ProviderTimestamp: bar.Start, FetchedAt: now, RequestID: req.RequestID,
		}
		if err := marketdata.ValidateNormalizedKline(row); err != nil {
			return nil, fmt.Errorf("%w: CNBC %s %s 合成的 5m 不合法：%v", marketdata.ErrProtocol, symbol, bar.Start.Format(time.RFC3339), err)
		}
		rows = append(rows, row)
	}
	if !rangeRequest && req.Limit > 0 && len(rows) > req.Limit {
		rows = rows[len(rows)-req.Limit:]
	}
	if len(rows) == 0 {
		return nil, marketdata.ErrNoClosedBar
	}
	return rows, nil
}

// validSymbol 报告代码是否只含大写字母、数字和点（带类别的代码保留点，例如 BRK.B；CNBC 不认 BRK_B）。
func validSymbol(symbol string) bool {
	if symbol == "" || len(symbol) > 12 {
		return false
	}
	for _, r := range symbol {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.') {
			return false
		}
	}
	return true
}

// fetchMinutes 请求一只标的在 [start, end)（UTC，按开始时刻）内的 1M。接口的终点含当分钟，所以终点写 end 前一分钟。
func (p *Provider) fetchMinutes(ctx context.Context, symbol string, start, end time.Time) ([]minuteBar, error) {
	last := end.Add(-time.Minute)
	if last.Before(start) {
		return nil, nil
	}
	path := barsPath + "/" + url.PathEscape(symbol) + "/1M/" + start.In(newYork).Format("20060102150405") + "/" + last.In(newYork).Format("20060102150405") + "/unadjusted/EST5EDT.json"
	ctx, finish := httpclient.TraceRequest(ctx, Host)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+path, nil)
	if err != nil {
		finish(nil, err)
		return nil, err
	}
	request.Header.Set("User-Agent", userAgent)
	request.Header.Set("Accept", "application/json")
	response, err := p.client.Do(request)
	if err != nil {
		finish(nil, err)
		return nil, fmt.Errorf("%w: %v", marketdata.ErrTimeout, err)
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		finish(response, &httpclient.StatusError{StatusCode: response.StatusCode})
		if response.StatusCode == http.StatusTooManyRequests {
			return nil, marketdata.ErrRateLimited
		}
		return nil, fmt.Errorf("%w: status=%d", marketdata.ErrHTTPStatus, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBody))
	finish(response, err)
	if err != nil {
		return nil, fmt.Errorf("%w: 读取返回内容：%v", marketdata.ErrProtocol, err)
	}
	minutes, err := parseMinutes(body, symbol)
	if err != nil {
		return nil, err
	}
	// 接口按纽约时间的分钟取，两端都含；这里按请求区间再裁一次，分窗请求时窗口边界不会重复。
	kept := minutes[:0]
	for _, minute := range minutes {
		if !minute.Start.Before(start) && minute.Start.Before(end) {
			kept = append(kept, minute)
		}
	}
	return kept, nil
}

// parseMinutes 解析 1M 的返回：{"barData":{"priceBars":[{"open":"347.56","high":…,"low":…,"close":…,"volume":1225445,
// "tradeTime":"20261123093000","tradeTimeinMills":"1795962600000"},…]}}。tradeTime 是纽约时间的开始时刻。
// 区间内没有成交时 priceBars 为空或没有这个字段；行必须按时刻严格递增。
func parseMinutes(body []byte, symbol string) ([]minuteBar, error) {
	var payload struct {
		BarData *struct {
			PriceBars []struct {
				Open      string          `json:"open"`
				High      string          `json:"high"`
				Low       string          `json:"low"`
				Close     string          `json:"close"`
				Volume    json.Number     `json:"volume"`
				TradeTime string          `json:"tradeTime"`
				Millis    json.RawMessage `json:"tradeTimeinMills"`
			} `json:"priceBars"`
		} `json:"barData"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("%w: CNBC %s 的返回不是 JSON：%v", marketdata.ErrProtocol, symbol, err)
	}
	if payload.BarData == nil {
		return nil, fmt.Errorf("%w: CNBC %s 的返回没有 barData", marketdata.ErrProtocol, symbol)
	}
	out := make([]minuteBar, 0, len(payload.BarData.PriceBars))
	for _, raw := range payload.BarData.PriceBars {
		start, err := time.ParseInLocation("20060102150405", strings.TrimSpace(raw.TradeTime), newYork)
		if err != nil {
			return nil, fmt.Errorf("%w: CNBC %s 的时刻 %q：%v", marketdata.ErrProtocol, symbol, raw.TradeTime, err)
		}
		bar := minuteBar{Start: start.UTC()}
		values := []*float64{&bar.Open, &bar.High, &bar.Low, &bar.Close}
		for index, text := range []string{raw.Open, raw.High, raw.Low, raw.Close} {
			value, parseErr := strconv.ParseFloat(strings.TrimSpace(text), 64)
			if parseErr != nil || value <= 0 {
				return nil, fmt.Errorf("%w: CNBC %s %s 的价格 %q 不对", marketdata.ErrProtocol, symbol, raw.TradeTime, text)
			}
			*values[index] = value
		}
		volume, err := strconv.ParseFloat(raw.Volume.String(), 64)
		if err != nil || volume < 0 {
			return nil, fmt.Errorf("%w: CNBC %s %s 的成交量 %q 不对", marketdata.ErrProtocol, symbol, raw.TradeTime, raw.Volume)
		}
		bar.Volume = volume
		if n := len(out); n > 0 && !bar.Start.After(out[n-1].Start) {
			return nil, fmt.Errorf("%w: CNBC %s 的 1M 时刻不递增：%s 在 %s 之后", marketdata.ErrProtocol, symbol, raw.TradeTime, out[n-1].Start.In(newYork).Format("20060102150405"))
		}
		out = append(out, bar)
	}
	return out, nil
}

// fiveMinute 是合成好的一根 5m。
type fiveMinute struct {
	Start, End             time.Time
	Open, High, Low, Close float64
	Volume, Amount         float64
}

// aggregate 把 1M 合成 5m：按开始时刻落进交易时段的桶（不在时段内的盘前、盘后与 16:00 那根丢掉），
// 开盘取桶里第一根 1M 的开盘、收盘取最后一根的收盘，最高、最低取极值，成交量求和，
// 成交额按每根 1M 的 成交量 ×（开 + 高 + 低 + 收）/ 4 求和。没有成交量的 1M 不参与；
// 一根 5m 里一根有成交的 1M 都没有就不出这一根。只返回 now 不早于结束时刻的根。
func aggregate(clock barclock.Clock, minutes []minuteBar, now time.Time) ([]fiveMinute, error) {
	var bars []fiveMinute
	for _, minute := range minutes {
		if minute.Volume <= 0 {
			continue
		}
		bucket, ok, err := clock.BucketAt(minute.Start)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		amount := minute.Volume * (minute.Open + minute.High + minute.Low + minute.Close) / 4
		if n := len(bars); n > 0 && bars[n-1].Start.Equal(bucket.Start) {
			bar := &bars[n-1]
			bar.High, bar.Low, bar.Close = max(bar.High, minute.High), min(bar.Low, minute.Low), minute.Close
			bar.Volume += minute.Volume
			bar.Amount += amount
			continue
		}
		bars = append(bars, fiveMinute{Start: bucket.Start.UTC(), End: bucket.End.UTC(), Open: minute.Open, High: minute.High, Low: minute.Low, Close: minute.Close, Volume: minute.Volume, Amount: amount})
	}
	closed := bars[:0]
	for _, bar := range bars {
		if !now.Before(bar.End) {
			closed = append(closed, bar)
		}
	}
	return closed, nil
}
