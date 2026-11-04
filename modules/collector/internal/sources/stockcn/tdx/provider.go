package tdx

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	commonsrc "github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn"
	"github.com/mooyang-code/moox/packages/barclock"
	tdxwire "github.com/mooyang-code/moox/packages/tdx"
)

type Config struct {
	Host              string
	Port              int
	Timeout           time.Duration
	Dial              tdxwire.DialFunc
	Now               func() time.Time
	RateLimit         marketdata.RateLimitPolicy
	MaxBarsPerRequest int
}

type Provider struct {
	host              string
	port              int
	timeout           time.Duration
	dial              tdxwire.DialFunc
	now               func() time.Time
	rateLimit         marketdata.RateLimitPolicy
	maxBarsPerRequest int
	// fetchPage 取一页 K 线（start 是从服务端最新一根往前数的偏移）；测试里替换成伪造的服务端。
	fetchPage func(ctx context.Context, market tdxwire.Market, code string, category tdxwire.KlineCategory, start, count int) ([]tdxwire.Bar, error)
}

const (
	// tdxMaxBarsPerPage 是协议允许的单页上限。
	tdxMaxBarsPerPage = 800
	stockCNMarket     = "stockcn"
)

func New(cfg Config) *Provider {
	if strings.TrimSpace(cfg.Host) == "" {
		// 2026-11-04 实测 jstdx.gtjas.com 返回的分钟线解码失败（minute datetime is truncated），shtdx.gtjas.com 正常。
		cfg.Host = "shtdx.gtjas.com"
	}
	if cfg.Port <= 0 {
		cfg.Port = 7709
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.RateLimit.RequestsPerSecond <= 0 {
		cfg.RateLimit = marketdata.RateLimitPolicy{
			RequestsPerSecond: 5,
			Burst:             2,
			MaxConcurrent:     1,
			Cooldown:          time.Second,
			RequestTimeout:    cfg.Timeout,
		}
	}
	if cfg.MaxBarsPerRequest <= 0 || cfg.MaxBarsPerRequest > tdxMaxBarsPerPage {
		cfg.MaxBarsPerRequest = tdxMaxBarsPerPage
	}
	provider := &Provider{
		host:              strings.TrimSpace(cfg.Host),
		port:              cfg.Port,
		timeout:           cfg.Timeout,
		dial:              cfg.Dial,
		now:               cfg.Now,
		rateLimit:         cfg.RateLimit,
		maxBarsPerRequest: cfg.MaxBarsPerRequest,
	}
	provider.fetchPage = provider.fetchPageOverNetwork
	return provider
}

func (p *Provider) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{
		ID:              "tdx",
		SourceID:        "normal_7709",
		DisplayName:     "TDX normal 7709",
		Hosts:           []string{p.host},
		ProtocolVariant: "tdx_normal",
		Transport:       "tcp",
		Port:            p.port,
		Status:          marketdata.SourceEnabled,
	}
}

func (p *Provider) KlineSpec() marketdata.KlineSpec {
	return marketdata.KlineSpec{
		Markets:           []string{"stockcn"},
		Exchanges:         []string{"XSHG", "XSHE", "XBSE"},
		Frequencies:       []string{"1m", "5m", "15m", "30m", "1h", "1d", "1w"},
		CompleteOHLCV:     true,
		HasAmount:         true,
		MaxBarsPerRequest: p.maxBarsPerRequest,
		// 日内 K 线的时间标签是收盘时刻（见 intradayBarStart）；日线及以上按交易日期取开盘日。
		TimestampMode: marketdata.TimestampModeClose,
		// 按偏移分页往回取。2026-11-04 实测 600519：5m 偏移 16000 根仍有数据（2025-05），1m 偏移 16000 根到 2026-07、
		// 40000 根已无数据。声明 5m 及更长周期 365 天、1m 90 天。
		History:            marketdata.KlineHistoryCapability{MaxLookback: 365 * 24 * time.Hour},
		HistoryByFrequency: map[string]marketdata.KlineHistoryCapability{"1m": {MaxLookback: 90 * 24 * time.Hour}},
		RateLimit:          p.rateLimit,
	}
}

func (p *Provider) FetchKlines(ctx context.Context, req marketdata.KlineRequest) ([]marketdata.NormalizedKline, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if req.SourceID != "" && !strings.EqualFold(strings.TrimSpace(req.SourceID), "normal_7709") {
		return nil, fmt.Errorf("%w: tdx source_id %q is not normal_7709", marketdata.ErrUnsupportedSymbol, req.SourceID)
	}
	category, err := categoryForFrequency(req.Frequency)
	if err != nil {
		return nil, err
	}
	market, err := marketForExchange(req.ExchangeID)
	if err != nil {
		return nil, err
	}
	providerSymbol, err := commonsrc.ProviderSymbol(req.SubjectID)
	if err != nil {
		return nil, err
	}
	code := providerSymbol[2:]
	now := p.now().UTC()
	offset, count, err := barWindow(req, now)
	if err != nil {
		return nil, err
	}
	bars, err := p.fetchRange(ctx, market, code, category, offset, count)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: fetch %s: %v", marketdata.ErrProtocol, providerSymbol, err)
	}
	rows, err := normalizeBars(req.SubjectID, providerSymbol, req.Frequency, bars, now, req.RequestID)
	if err != nil {
		return nil, err
	}
	// 交易时段内服务端最新一根是还没收盘的那根：不返回。
	closed := rows[:0]
	for _, row := range rows {
		if !row.BarEnd.After(now) {
			closed = append(closed, row)
		}
	}
	if len(closed) == 0 {
		return nil, marketdata.ErrNoClosedBar
	}
	return closed, nil
}

// barWindow 把请求换成通达信的取数窗口：offset 是从服务端最新一根往前数的偏移，count 是根数。
//
// 服务端最新一根：交易时段内是正在形成的那根，休市时是最近收盘的那根（午休时即上午最后一根）。
// 有起始时间时按 barclock 交易序号算出目标区间离最新一根有多远，多取一根较新的，以容忍服务端还没生成新一根；
// 没有起始时间时取最新 Limit 根，也多取一根（可能是未收盘那根）。日线及以上只取最新 Limit 根。
// 停牌的标的缺少中间的 K 线，按市场的交易序号算出的偏移会偏大，这种情况由覆盖校验报错。
func barWindow(req marketdata.KlineRequest, now time.Time) (int, int, error) {
	frequency, _ := req.FrequencyValue()
	if req.StartTime.IsZero() || frequency.Duration() <= 0 || frequency.Duration() >= 24*time.Hour {
		return 0, req.Limit + 1, nil
	}
	clock, err := markets.Clock(stockCNMarket, req.Frequency)
	if err != nil {
		return 0, 0, fmt.Errorf("%w: %v", marketdata.ErrUnsupportedFrequency, err)
	}
	ordinalAt := func(b barclock.Bucket, err error) (int64, error) {
		if err != nil {
			return 0, err
		}
		return clock.Ordinal(b)
	}
	newest, err := ordinalAt(clock.LatestClosed(now))
	if err != nil {
		return 0, 0, err
	}
	if _, forming, err := clock.BucketAt(now); err != nil {
		return 0, 0, err
	} else if forming {
		newest++
	}
	end := newest
	if !req.EndTime.IsZero() && req.EndTime.Before(now) {
		if end, err = ordinalAt(clock.LatestClosed(req.EndTime)); err != nil {
			return 0, 0, err
		}
	}
	before, err := clock.LatestClosed(req.StartTime)
	if err != nil {
		return 0, 0, err
	}
	start, err := ordinalAt(clock.Next(before))
	if err != nil {
		return 0, 0, err
	}
	if end < start {
		return 0, 0, fmt.Errorf("%w: [%s, %s) 内没有交易时段的 K 线", marketdata.ErrHistoryCoverage, req.StartTime.UTC().Format(time.RFC3339), req.EndTime.UTC().Format(time.RFC3339))
	}
	offset := max(newest-end-1, 0)
	return int(offset), int(newest-offset-start) + 1, nil
}

// fetchRange 从偏移 offset 起往前取 count 根，超过单页上限时分页，按时间升序返回。
func (p *Provider) fetchRange(ctx context.Context, market tdxwire.Market, code string, category tdxwire.KlineCategory, offset, count int) ([]tdxwire.Bar, error) {
	if count <= 0 {
		return nil, fmt.Errorf("%w: tdx bar count must be positive", marketdata.ErrInvalidRequest)
	}
	pages := (count + p.maxBarsPerRequest - 1) / p.maxBarsPerRequest
	var out []tdxwire.Bar
	// 从最旧的一页取起，拼起来就是升序；页与页之间服务端若生成了新的一根，偏移会错开一根，按时间去重。
	for page := pages - 1; page >= 0; page-- {
		size := min(p.maxBarsPerRequest, count-page*p.maxBarsPerRequest)
		bars, err := p.fetchPage(ctx, market, code, category, offset+page*p.maxBarsPerRequest, size)
		if err != nil {
			return nil, err
		}
		for _, bar := range bars {
			if len(out) == 0 || bar.Time.After(out[len(out)-1].Time) {
				out = append(out, bar)
			}
		}
	}
	return out, nil
}

// fetchPageOverNetwork 每页单独建连：实测 shtdx.gtjas.com 一个连接只回应一次 K 线请求，同一连接上的第二次请求读超时。
func (p *Provider) fetchPageOverNetwork(ctx context.Context, market tdxwire.Market, code string, category tdxwire.KlineCategory, start, count int) ([]tdxwire.Bar, error) {
	client, err := tdxwire.NewClient(tdxwire.ClientOptions{
		Host: p.host, Port: p.port, Variant: tdxwire.ProtocolNormal,
		Timeout: p.timeout, Dial: p.dial,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: create tdx client: %v", marketdata.ErrTCP, err)
	}
	if err := client.Connect(ctx); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("%w: %v", marketdata.ErrTCP, err)
	}
	defer client.Close()
	return (&tdxwire.NormalClient{Client: client}).SecurityBars(ctx, market, code, category, start, count, false)
}

func categoryForFrequency(raw string) (tdxwire.KlineCategory, error) {
	parsed, err := marketdata.ParseFrequency(raw)
	if err != nil {
		return 0, fmt.Errorf("%w: tdx frequency %q", marketdata.ErrUnsupportedFrequency, raw)
	}
	switch parsed {
	case marketdata.FrequencyMinute:
		return tdxwire.Category1Min, nil
	case marketdata.Frequency5Min:
		return tdxwire.Category5Min, nil
	case marketdata.Frequency15Min:
		return tdxwire.Category15Min, nil
	case marketdata.Frequency30Min:
		return tdxwire.Category30Min, nil
	case marketdata.FrequencyHour:
		return tdxwire.Category60Min, nil
	case marketdata.FrequencyDay:
		return tdxwire.CategoryDay, nil
	case marketdata.FrequencyWeek:
		return tdxwire.CategoryWeek, nil
	case marketdata.FrequencyMonth:
		return tdxwire.CategoryMonth, nil
	default:
		return 0, fmt.Errorf("%w: tdx frequency %q", marketdata.ErrUnsupportedFrequency, raw)
	}
}

func marketForExchange(exchange marketdata.ExchangeID) (tdxwire.Market, error) {
	switch strings.ToUpper(strings.TrimSpace(string(exchange))) {
	case "XSHG":
		return tdxwire.MarketSH, nil
	case "XSHE":
		return tdxwire.MarketSZ, nil
	case "XBSE":
		return tdxwire.MarketBJ, nil
	default:
		return 0, fmt.Errorf("%w: tdx exchange %q", marketdata.ErrUnsupportedSymbol, exchange)
	}
}

func normalizeBars(subjectID, providerSymbol, frequency string, bars []tdxwire.Bar, fetchedAt time.Time, requestID string) ([]marketdata.NormalizedKline, error) {
	parsedFrequency, err := marketdata.ParseFrequency(frequency)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", marketdata.ErrUnsupportedFrequency, err)
	}
	if parsedFrequency.Duration() <= 0 && parsedFrequency != marketdata.FrequencyMonth {
		return nil, fmt.Errorf("%w: tdx frequency %q has no duration", marketdata.ErrUnsupportedFrequency, frequency)
	}
	daily := parsedFrequency == marketdata.FrequencyDay || parsedFrequency == marketdata.FrequencyWeek || parsedFrequency == marketdata.FrequencyMonth
	location, err := markets.Location(stockCNMarket)
	if err != nil {
		return nil, err
	}
	var clock barclock.Clock
	if !daily {
		if clock, err = markets.Clock(stockCNMarket, frequency); err != nil {
			return nil, fmt.Errorf("%w: %v", marketdata.ErrUnsupportedFrequency, err)
		}
	}
	rows := make([]marketdata.NormalizedKline, 0, len(bars))
	var previous time.Time
	for index, bar := range bars {
		barStart := bar.Time
		if !daily {
			barStart, err = intradayBarStart(clock, location, bar.Time)
			if err != nil {
				return nil, fmt.Errorf("tdx bar %d: %w", index, err)
			}
		}
		if parsedFrequency == marketdata.FrequencyDay || parsedFrequency == marketdata.FrequencyWeek || parsedFrequency == marketdata.FrequencyMonth {
			local := bar.Time.In(location)
			if parsedFrequency == marketdata.FrequencyMonth {
				barStart = time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, location)
			} else {
				barStart = time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
			}
		}
		barStart = barStart.UTC()
		if !previous.IsZero() && !barStart.After(previous) {
			return nil, fmt.Errorf("%w: tdx bars are not strictly increasing at index %d", marketdata.ErrProtocol, index)
		}
		row := marketdata.NormalizedKline{
			SubjectID:         subjectID,
			ProviderID:        "tdx",
			SourceID:          "normal_7709",
			ProviderSymbol:    providerSymbol,
			Frequency:         string(parsedFrequency),
			BarStart:          barStart,
			BarEnd:            parsedFrequency.BarEnd(barStart),
			Open:              bar.Open,
			High:              bar.High,
			Low:               bar.Low,
			Close:             bar.Close,
			VolumeShares:      bar.Volume,
			AmountCNY:         bar.Amount,
			ProviderTimestamp: bar.Time.UTC(),
			FetchedAt:         fetchedAt,
			RequestID:         requestID,
		}
		if err := marketdata.ValidateNormalizedKline(row); err != nil {
			return nil, fmt.Errorf("tdx bar %d: %w", index, err)
		}
		rows = append(rows, row)
		previous = barStart
	}
	return rows, nil
}

// intradayBarStart 把通达信日内 K 线的时间标签换成开盘时刻。
//
// 标签是收盘时刻：2026-11-04 实测 600519 的 5m，首根 09:35、上午末根 11:30、下午首根 13:05、末根 15:00，
// 与东方财富同一根逐根一致（成交量单位都是股，首根都含 09:25 开盘集合竞价）。
// 例外：午休期间上午最后一根被标成下午开盘的 13:00（同日实测：11:25 之后紧跟 13:00，其 OHLCV 与东方财富 11:30 那根一致）。
// 标签恰好是某个交易时段的开盘时刻时，它表示同一交易日里前一根，而不是落在休市里的桶。
func intradayBarStart(clock barclock.Clock, location *time.Location, label time.Time) (time.Time, error) {
	start := label.Add(-clock.Period()).UTC()
	if bucket, ok, err := clock.BucketAt(start); err != nil {
		return time.Time{}, err
	} else if ok && bucket.Start.Equal(start) {
		return start, nil
	}
	bucket, ok, err := clock.BucketAt(label)
	if err != nil || !ok || !bucket.Start.Equal(label.UTC()) {
		// 不是交易桶：原样换算，交给公共写入边界按请求区间过滤。
		return start, err
	}
	previous, err := clock.Prev(bucket)
	if err != nil {
		return time.Time{}, err
	}
	if previous.Start.In(location).Format(time.DateOnly) != label.In(location).Format(time.DateOnly) {
		return start, nil
	}
	return previous.Start, nil
}
