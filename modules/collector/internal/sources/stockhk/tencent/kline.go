package tencent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/mooyang-code/moox/packages/barclock"
)

// 港股 5m K 线的备源：腾讯没有港股的分钟 K 线（A 股用的 mkline 接口对港股代码报 param error），
// 只有当天的实时分时 hkMinute：每分钟一行“时刻 价格 累计成交量 累计成交额”，这里把它合成 5m。
// 2026-11-23 用 6 只、223 根与东方财富比对：收盘价 222 根一致；成交量、成交额偏大 3%～7%（东方财富的 5m 不含碎股）；
// 开盘、最高、最低是近似值（每分钟只有最后一个价格，分钟内的极值取不到）。所以只做东方财富失败时的回落来源。
const (
	// KlineSourceID 是 K 线来源在行情源目录和路由文件里的 source_id，与指数成分来源（DefaultSourceID）分开登记。
	KlineSourceID = "stockhk_minute_http"

	klineHost        = "ifzq.gtimg.cn"
	defaultKlineBase = "https://" + klineHost
	minutePath       = "/appstock/app/hkMinute/query"
	// klineLookback 是能取到的回看范围：接口只给当天，保持 24 小时；更早的根返回里没有，由候选链上的覆盖校验判失败。
	klineLookback = 24 * time.Hour
	// klineMaxBars 是一次响应最多合成的根数：一天 66 根。
	klineMaxBars = 66
)

// klineFrequency 是唯一支持的周期。
var klineFrequency = marketdata.Frequency5Min

// hongKong 是分时时刻的时区（与北京时间相同，没有夏令时）。
var hongKong = func() *time.Location {
	location, err := time.LoadLocation("Asia/Hong_Kong")
	if err != nil {
		panic(err)
	}
	return location
}()

// KlineConfig 里的地址、source_id、限速来自路由文件与数据源配置文件。
type KlineConfig struct {
	BaseURL    string
	SourceID   string
	HTTPClient *http.Client
	Now        func() time.Time
	RateLimit  marketdata.RateLimitPolicy
}

// KlineProvider 是腾讯分时合成的港股 5m K 线来源。
type KlineProvider struct {
	baseURL   string
	sourceID  string
	client    *http.Client
	now       func() time.Time
	rateLimit marketdata.RateLimitPolicy
}

func NewKline(cfg KlineConfig) *KlineProvider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultKlineBase
	}
	if strings.TrimSpace(cfg.SourceID) == "" {
		cfg.SourceID = KlineSourceID
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.RateLimit.RequestsPerSecond <= 0 {
		cfg.RateLimit = marketdata.RateLimitPolicy{RequestsPerSecond: 5, Burst: 2, MaxConcurrent: 1, Cooldown: time.Second, RequestTimeout: 5 * time.Second}
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &KlineProvider{baseURL: strings.TrimRight(cfg.BaseURL, "/"), sourceID: strings.ToLower(strings.TrimSpace(cfg.SourceID)), client: cfg.HTTPClient, now: cfg.Now, rateLimit: cfg.RateLimit}
}

func (p *KlineProvider) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{ID: providerID, SourceID: p.sourceID, DisplayName: "Tencent HK Minute", Hosts: []string{klineHost}, ProtocolVariant: "http", Transport: "https", Port: 443}
}

func (p *KlineProvider) KlineSpec() marketdata.KlineSpec {
	return marketdata.KlineSpec{
		Markets:     []string{marketID},
		Exchanges:   []string{exchange},
		Frequencies: []string{string(klineFrequency)},
		// 开盘、最高、最低由分钟价格近似得到。
		CompleteOHLCV:     false,
		HasAmount:         true,
		MaxBarsPerRequest: klineMaxBars,
		TimestampMode:     marketdata.TimestampModeClose,
		History:           marketdata.KlineHistoryCapability{MaxLookback: klineLookback},
		RateLimit:         p.rateLimit,
	}
}

// minuteRow 是分时的一行：At 是这一分钟的结束时刻（0931 是 09:30～09:31 这一分钟，0930 是开盘那一刻、含开盘竞价），
// Price 是这一分钟最后的成交价，Volume、Amount 是当天到这一分钟为止的累计值。
type minuteRow struct {
	At     time.Time
	Price  float64
	Volume float64
	Amount float64
}

// FetchKlines 取当天分时并合成 5m，只返回已经收盘、且分时已经走到收盘时刻的根；请求区间由公共写入边界过滤。
func (p *KlineProvider) FetchKlines(ctx context.Context, req marketdata.KlineRequest) ([]marketdata.NormalizedKline, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	frequency, _ := req.FrequencyValue() // Validate 已校验
	if frequency != klineFrequency {
		return nil, fmt.Errorf("%w: 腾讯港股分时只合成 5m，不支持 %s", marketdata.ErrUnsupportedFrequency, req.Frequency)
	}
	code := strings.TrimSpace(req.ProviderSymbol)
	if !isCode(code) {
		return nil, fmt.Errorf("%w: 港股代码 %q 不是五位数字", marketdata.ErrUnsupportedSymbol, req.ProviderSymbol)
	}
	day, rows, err := p.fetchMinutes(ctx, "hk"+code)
	if err != nil {
		return nil, err
	}
	clock, err := markets.Clock(marketID, string(frequency))
	if err != nil {
		return nil, err
	}
	now := p.now().UTC()
	bars, err := aggregateMinutes(clock, rows, now)
	if err != nil {
		return nil, err
	}
	out := make([]marketdata.NormalizedKline, 0, len(bars))
	for _, bar := range bars {
		// 没有成交的根不返回，与东方财富一致（东方财富要等这一根第一笔成交才出这一根）：腾讯对没有成交的分钟照样按上一价格给一行，
		// 停牌的标的当天也给满 332 行、成交量全是 0（2026-11-23 实测 00204），不能当成真实的 K 线写入。
		if bar.volume <= 0 {
			continue
		}
		row := marketdata.NormalizedKline{
			SubjectID: req.SubjectID, ProviderID: providerID, SourceID: p.sourceID, ProviderSymbol: code, Frequency: string(frequency),
			BarStart: bar.bucket.Start.UTC(), BarEnd: bar.bucket.End.UTC(),
			Open: bar.open, High: bar.high, Low: bar.low, Close: bar.close, Volume: bar.volume, Amount: bar.amount,
			ProviderTimestamp: bar.bucket.End.UTC(), FetchedAt: now, RequestID: req.RequestID,
		}
		if err := marketdata.ValidateNormalizedKline(row); err != nil {
			return nil, fmt.Errorf("%w: 腾讯港股分时 %s %s 合成的 5m 不合法：%v", marketdata.ErrProtocol, code, bar.bucket.Start.Format(time.RFC3339), err)
		}
		out = append(out, row)
	}
	// 请求的区间在返回的这一天里、分时已经走过区间的终点，区间里却没有有成交的根：腾讯证明了这段时间没有成交，
	// 报“没有已收盘的根”，与东方财富对没有成交的根的结论一致。否则候选链按“区间内没有根”判覆盖不到，
	// 补采拿到的是主源的断连错误，记不了“行情源确认没有”，这一块每轮失败、自动补采的水位停住（2026-11-23 00002 的 13:10）。
	if len(out) > 0 && !req.StartTime.IsZero() && !req.EndTime.IsZero() && sameDay(req.StartTime, day) &&
		!rows[len(rows)-1].At.Before(req.EndTime) && !now.Before(req.EndTime) && !hasBarIn(out, req.StartTime, req.EndTime) {
		return nil, fmt.Errorf("%w: 腾讯港股分时 %s 在 %s～%s 没有成交", marketdata.ErrNoClosedBar, code, req.StartTime.Format(time.RFC3339), req.EndTime.Format(time.RFC3339))
	}
	if len(out) == 0 {
		// 返回的是别的交易日（长期停牌的标的返回最后交易日的分时，2026-11-23 实测 00232 返回 10-12；开盘前补上一交易日时
		// 已经换成当天）：证明不了请求的那天有没有成交，报覆盖不到，不报“没有已收盘的根”——后者会被补采当成行情源确认没有。
		if !req.StartTime.IsZero() && !sameDay(req.StartTime, day) {
			return nil, fmt.Errorf("%w: 腾讯港股分时 %s 返回的是 %s，不是请求的那天", marketdata.ErrHistoryCoverage, code, day.Format("2006-01-02"))
		}
		return nil, marketdata.ErrNoClosedBar
	}
	return out, nil
}

// hasBarIn 报告 rows 里有没有开始时刻落在 [start, end) 的根。
func hasBarIn(rows []marketdata.NormalizedKline, start, end time.Time) bool {
	for _, row := range rows {
		if !row.BarStart.Before(start) && row.BarStart.Before(end) {
			return true
		}
	}
	return false
}

// sameDay 报告 t 与 day（香港时间零点）是不是香港时间的同一天。
func sameDay(t, day time.Time) bool {
	y1, m1, d1 := t.In(hongKong).Date()
	y2, m2, d2 := day.In(hongKong).Date()
	return y1 == y2 && m1 == m2 && d1 == d2
}

// fetchMinutes 请求一只标的的分时（接口一次只认一个代码），返回分时所属的交易日（香港时间零点）和各行。
func (p *KlineProvider) fetchMinutes(ctx context.Context, symbol string) (time.Time, []minuteRow, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+minutePath+"?"+url.Values{"code": {symbol}}.Encode(), nil)
	if err != nil {
		return time.Time{}, nil, err
	}
	request.Header.Set("User-Agent", "moox-collector/1.0")
	response, err := p.client.Do(request)
	if err != nil {
		return time.Time{}, nil, fmt.Errorf("%w: %v", marketdata.ErrTimeout, err)
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode == http.StatusTooManyRequests:
		return time.Time{}, nil, marketdata.ErrRateLimited
	case response.StatusCode != http.StatusOK:
		return time.Time{}, nil, fmt.Errorf("%w: status=%d", marketdata.ErrHTTPStatus, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return time.Time{}, nil, fmt.Errorf("%w: 读返回内容：%v", marketdata.ErrProtocol, err)
	}
	return parseMinutes(body, symbol)
}

// parseMinutes 解析 hkMinute 的返回：{"code":0,"data":{"hk00700":{"data":{"date":"20261123","data":["0930 335.000 1261051 422889153.600",...]}}}}。
// 时刻按香港时间解释（与北京时间相同）；行必须按时刻递增、累计值不减，否则按协议错误处理。
func parseMinutes(body []byte, symbol string) (time.Time, []minuteRow, error) {
	var payload struct {
		Code *int            `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return time.Time{}, nil, fmt.Errorf("%w: %v", marketdata.ErrProtocol, err)
	}
	if payload.Code == nil || *payload.Code != 0 {
		return time.Time{}, nil, fmt.Errorf("%w: 腾讯港股分时 %s 返回失败 code=%v msg=%q", marketdata.ErrProtocol, symbol, codeValue(payload.Code), payload.Msg)
	}
	var data map[string]struct {
		Data struct {
			Date string   `json:"date"`
			Data []string `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload.Data, &data); err != nil {
		return time.Time{}, nil, fmt.Errorf("%w: 腾讯港股分时 %s 的 data：%v", marketdata.ErrProtocol, symbol, err)
	}
	item, ok := data[symbol]
	if !ok {
		return time.Time{}, nil, fmt.Errorf("%w: 腾讯港股分时没有返回 %s", marketdata.ErrProtocol, symbol)
	}
	day, err := time.ParseInLocation("20060102", strings.TrimSpace(item.Data.Date), hongKong)
	if err != nil {
		return time.Time{}, nil, fmt.Errorf("%w: 腾讯港股分时 %s 的日期 %q：%v", marketdata.ErrProtocol, symbol, item.Data.Date, err)
	}
	rows := make([]minuteRow, 0, len(item.Data.Data))
	for _, line := range item.Data.Data {
		fields := strings.Fields(line)
		if len(fields) < 4 || len(fields[0]) != 4 {
			return time.Time{}, nil, fmt.Errorf("%w: 腾讯港股分时 %s 的行 %q 格式不对", marketdata.ErrProtocol, symbol, line)
		}
		hour, hourErr := strconv.Atoi(fields[0][:2])
		minute, minuteErr := strconv.Atoi(fields[0][2:])
		price, priceErr := strconv.ParseFloat(fields[1], 64)
		volume, volumeErr := strconv.ParseFloat(fields[2], 64)
		amount, amountErr := strconv.ParseFloat(fields[3], 64)
		if hourErr != nil || minuteErr != nil || priceErr != nil || volumeErr != nil || amountErr != nil || hour > 23 || minute > 59 || price <= 0 || volume < 0 || amount < 0 {
			return time.Time{}, nil, fmt.Errorf("%w: 腾讯港股分时 %s 的行 %q 数值不对", marketdata.ErrProtocol, symbol, line)
		}
		row := minuteRow{At: day.Add(time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute).UTC(), Price: price, Volume: volume, Amount: amount}
		if n := len(rows); n > 0 {
			prev := rows[n-1]
			if !row.At.After(prev.At) || row.Volume < prev.Volume || row.Amount < prev.Amount {
				return time.Time{}, nil, fmt.Errorf("%w: 腾讯港股分时 %s 的行 %q 时刻不递增或累计值变小", marketdata.ErrProtocol, symbol, line)
			}
		}
		rows = append(rows, row)
	}
	return day, rows, nil
}

func codeValue(code *int) any {
	if code == nil {
		return nil
	}
	return *code
}

// minuteBar 是合成中的一根 5m。
type minuteBar struct {
	bucket                 barclock.Bucket
	open, high, low, close float64
	volume, amount         float64
}

// aggregateMinutes 把分时合成 5m：
//   - 一行属于“这一分钟开始时刻”所在的桶；开始时刻不在交易时段时（09:30、13:00 那一行）属于结束时刻所在的桶，
//     两者都不在时段内（收市竞价写在 16:00 之后的行）并进前一根。
//   - 开盘价取这一根开始那一刻的价格（上一根最后一行的价格；09:30 那根是开盘那一行），收盘价取这一根最后一行；
//     最高、最低取开盘价与这一根各行价格的极值。
//   - 成交量、成交额是这一根各行累计值的增量之和，当天第一行（含开盘竞价）的增量是它本身。
//   - 只返回已经收盘（now 不早于结束时刻）、且分时已经走到结束时刻（有不早于结束时刻的行）的根。
func aggregateMinutes(clock barclock.Clock, rows []minuteRow, now time.Time) ([]minuteBar, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	latest := rows[len(rows)-1].At
	var bars []minuteBar
	var prev minuteRow
	for index, row := range rows {
		bucket, ok, err := clock.BucketAt(row.At.Add(-time.Minute))
		if err == nil && !ok {
			bucket, ok, err = clock.BucketAt(row.At)
		}
		if err != nil {
			return nil, err
		}
		volume, amount, open := row.Volume, row.Amount, row.Price
		if index > 0 {
			volume, amount, open = row.Volume-prev.Volume, row.Amount-prev.Amount, prev.Price
		}
		prev = row
		switch {
		case ok && (len(bars) == 0 || !bars[len(bars)-1].bucket.Start.Equal(bucket.Start)):
			bars = append(bars, minuteBar{bucket: bucket, open: open, high: max(open, row.Price), low: min(open, row.Price), close: row.Price, volume: volume, amount: amount})
		case len(bars) > 0:
			bar := &bars[len(bars)-1]
			bar.high, bar.low, bar.close = max(bar.high, row.Price), min(bar.low, row.Price), row.Price
			bar.volume += volume
			bar.amount += amount
		}
	}
	closed := bars[:0]
	for _, bar := range bars {
		if !now.Before(bar.bucket.End) && !latest.Before(bar.bucket.End) {
			closed = append(closed, bar)
		}
	}
	return closed, nil
}
