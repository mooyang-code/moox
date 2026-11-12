package eastmoney

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/eastmoneykline"
	commonsrc "github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn"
	"github.com/mooyang-code/moox/packages/cnstock"
)

type Config struct {
	// BaseURL 是标的列表（clist）的地址；KlineBaseURL 是 K 线的地址。两者不同：5m K 线只在 push2his 有数据。
	BaseURL                  string
	KlineBaseURL             string
	KlineEndpoint            string
	SourceID                 string
	HTTPClient               *http.Client
	Now                      func() time.Time
	InstrumentRequestTimeout time.Duration
	RateLimit                marketdata.RateLimitPolicy
	MaxBarsPerRequest        int
}

type Provider struct {
	baseURL string
	client  *http.Client
	now     func() time.Time
	// klines 是东方财富 K 线的共用代码，这里只给出 A 股自己的部分：secid 的写法、成交量单位、时区、支持的周期。
	klines              *eastmoneykline.Client
	rateLimit           marketdata.RateLimitPolicy
	instrumentRateLimit marketdata.RateLimitPolicy
}

func New(cfg Config) *Provider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = cnstock.EastMoneyPushBase
	}
	if strings.TrimSpace(cfg.SourceID) == "" {
		cfg.SourceID = "stockcn_http"
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.RateLimit.RequestsPerSecond <= 0 {
		cfg.RateLimit = eastmoneykline.DefaultRateLimit()
	}
	if cfg.InstrumentRequestTimeout <= 0 {
		cfg.InstrumentRequestTimeout = 15 * time.Second
	}
	if cfg.HTTPClient == nil {
		// Keep the HTTP transport at least as patient as the instrument page
		// guard; otherwise a configured 15-second guard is silently truncated.
		cfg.HTTPClient = &http.Client{Timeout: cfg.InstrumentRequestTimeout}
	}
	instrumentRateLimit := cfg.RateLimit
	instrumentRateLimit.RequestTimeout = cfg.InstrumentRequestTimeout
	klines := eastmoneykline.New(eastmoneykline.Config{
		BaseURL: cfg.KlineBaseURL, Endpoint: cfg.KlineEndpoint, ProviderID: providerID, SourceID: cfg.SourceID,
		HTTPClient: cfg.HTTPClient, Now: cfg.Now, MaxBarsPerRequest: cfg.MaxBarsPerRequest,
		Frequencies: klineFrequencies, SecID: commonsrc.EastMoneySecID, VolumeMultiplier: volumeMultiplier,
		Location: klineLocation, DailyClose: 15 * time.Hour,
	})
	return &Provider{baseURL: strings.TrimRight(cfg.BaseURL, "/"), client: cfg.HTTPClient, now: cfg.Now, klines: klines, rateLimit: cfg.RateLimit, instrumentRateLimit: instrumentRateLimit}
}

func (p *Provider) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{ID: providerID, SourceID: p.klines.SourceID(), DisplayName: "EastMoney", Hosts: []string{eastMoneyPushHost, eastmoneykline.Host}, ProtocolVariant: "http", Transport: "https", Port: 443, Status: marketdata.SourceEnabled}
}

func (p *Provider) KlineSpec() marketdata.KlineSpec {
	return marketdata.KlineSpec{
		Markets:           []string{"stockcn"},
		Exchanges:         []string{"XSHG", "XSHE", "XBSE"},
		Frequencies:       slices.Clone(klineFrequencies),
		CompleteOHLCV:     true,
		HasAmount:         true,
		MaxBarsPerRequest: p.klines.MaxBarsPerRequest(),
		// 时间标签是 K 线的收盘时刻（2026-11-04 实测：1m 首根 09:31、5m 首根 09:35，末根 11:30、15:00，
		// 午后首根 13:01 / 13:05），首根含 09:25 开盘集合竞价的成交。
		TimestampMode: marketdata.TimestampModeClose,
		// 1m 只能取到当天：保持 24 小时。5m 实测能取到约 1500 根（约 31 个交易日），保守声明 30 个自然日。
		// 日线（klt=101）带 beg、end 时返回区间内的全部交易日、不受 lmt 限制，能取到上市首日（2026-11-11 实测
		// 600519 取 2018-01-01～2026-10-31 一次返回 2140 根；300750 从 2018-06-11 上市首日起）：声明任意区间。
		History: marketdata.KlineHistoryCapability{MaxLookback: 24 * time.Hour},
		HistoryByFrequency: map[string]marketdata.KlineHistoryCapability{
			"5m": {MaxLookback: 30 * 24 * time.Hour},
			"1d": {SupportsArbitraryRange: true},
		},
		RateLimit: p.rateLimit,
	}
}

func (p *Provider) InstrumentSpec() marketdata.InstrumentSpec {
	return marketdata.InstrumentSpec{
		Markets:      []string{"stockcn"},
		Exchanges:    []string{"XSHG", "XSHE", "XBSE"},
		FullSnapshot: true,
		PageSize:     500,
		RateLimit:    p.instrumentRateLimit,
	}
}

// FetchKlines 取 K 线：请求、解析、收盘标签换开盘键都在共用代码里（见 eastmoneykline）。
func (p *Provider) FetchKlines(ctx context.Context, req marketdata.KlineRequest) ([]marketdata.NormalizedKline, error) {
	return p.klines.FetchKlines(ctx, req)
}

func (p *Provider) FetchInstrumentSnapshot(ctx context.Context, req marketdata.InstrumentRequest) (marketdata.InstrumentSnapshot, error) {
	marketID := strings.TrimSpace(string(req.MarketID))
	if marketID == "" {
		marketID = "stockcn"
	}
	if marketID != "stockcn" {
		return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: market_id %q is unsupported", marketdata.ErrInvalidRequest, req.MarketID)
	}
	fetchedAt := req.SnapshotAt.UTC()
	if req.SnapshotAt.IsZero() {
		fetchedAt = p.now().UTC()
	}
	snapshot, _, err := p.fetchClistSnapshot(ctx, eastMoneyAShareFilter, p.InstrumentSpec().PageSize, fetchedAt)
	return snapshot, err
}

// IndexCodes 返回登记了成分板块的指数代码。
func (p *Provider) IndexCodes() []string {
	codes := make([]string, 0, len(eastMoneyIndexBoards))
	for code := range eastMoneyIndexBoards {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	return codes
}

// FetchIndexConstituents 取指数的全部成分股：与全部 A 股同一个 clist 列表请求，筛选条件换成该指数的成分板块。
// 条数必须等于接口自报的 total，翻页不完整就报错。
func (p *Provider) FetchIndexConstituents(ctx context.Context, indexCode string) (marketdata.InstrumentSnapshot, error) {
	board, ok := eastMoneyIndexBoards[strings.TrimSpace(indexCode)]
	if !ok {
		return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: 东方财富没有登记指数 %q 的成分板块", marketdata.ErrInvalidRequest, indexCode)
	}
	snapshot, total, err := p.fetchClistSnapshot(ctx, "b:"+board, cnstock.EastMoneyClistMaxPageSize, p.now().UTC())
	if err != nil {
		return marketdata.InstrumentSnapshot{}, fmt.Errorf("指数 %s 成分股：%w", indexCode, err)
	}
	if total <= 0 || len(snapshot.Instruments) != total {
		return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: 指数 %s 成分股取到 %d 只，接口报的总数是 %d", marketdata.ErrProtocol, indexCode, len(snapshot.Instruments), total)
	}
	return snapshot, nil
}

// fetchClistSnapshot 按筛选条件 fs 翻页取完整的标的列表，返回快照与接口自报的总数（没报时为 0）。
// 翻页、主备地址切换和完整性校验在 packages/cnstock，这里只把每一条换成采集的标的并映射错误类型。
func (p *Provider) fetchClistSnapshot(ctx context.Context, fs string, pageSize int, fetchedAt time.Time) (marketdata.InstrumentSnapshot, int, error) {
	builder := commonsrc.NewInstrumentSnapshotBuilder(p.Descriptor().ID, "stockcn", fetchedAt)
	result, err := cnstock.FetchClist(ctx, cnstock.ClistQuery{
		BaseURL: p.baseURL, Filter: fs, PageSize: pageSize, HTTPClient: p.client, UserAgent: "moox-collector/1.0",
	}, func(item cnstock.ClistItem) error {
		instrument, err := commonsrc.InstrumentFromFields(item.Code, item.Symbol, item.Market, item.Name, item.Status)
		if err != nil {
			return err
		}
		return builder.Add(instrument)
	})
	if err != nil {
		return marketdata.InstrumentSnapshot{}, 0, collectorError(err)
	}
	for range result.Pages {
		builder.NextPage()
	}
	snapshot, err := builder.Snapshot()
	return snapshot, result.Total, err
}

// collectorError 把 cnstock 的错误类别映射回采集的错误类型；采集自己产生的错误（标的转换失败等）原样返回。
func collectorError(err error) error {
	var statusErr *cnstock.HTTPStatusError
	switch {
	case errors.As(err, &statusErr):
		return fmt.Errorf("%w: status=%d", marketdata.ErrHTTPStatus, statusErr.Status)
	case errors.Is(err, cnstock.ErrRateLimited):
		return marketdata.ErrRateLimited
	case errors.Is(err, cnstock.ErrTimeout):
		return fmt.Errorf("%w: %v", marketdata.ErrTimeout, err)
	case errors.Is(err, cnstock.ErrProtocol):
		return fmt.Errorf("%w: %v", marketdata.ErrProtocol, err)
	}
	return err
}

const (
	providerID        = "eastmoney"
	eastMoneyPushHost = "push2.eastmoney.com"
	// eastMoneyAShareFilter 是全部 A 股的 clist 筛选条件：深主板、创业板、沪主板。
	eastMoneyAShareFilter = "m:0 t:6,m:0 t:80,m:1 t:2"
	// 成交量单位是手：同一根 5m 东方财富 6735、通达信 673500 股（2026-11-04 实测 600519），乘 100 换成股。
	volumeMultiplier = 100
)

// klineFrequencies 是 A 股支持的 K 线周期。
var klineFrequencies = []string{"1m", "5m", "1d"}

// klineLocation 是 K 线时间标签的时区。
var klineLocation = mustLoadLocation("Asia/Shanghai")

func mustLoadLocation(name string) *time.Location {
	location, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return location
}

// eastMoneyIndexBoards 是指数代码到东方财富成分板块的对照：沪深 300 是 BK0500（板块名 HS300_，
// 2026-11-04 实测 clist fs=b:BK0500 返回 total=300、三页各 100 条）。
var eastMoneyIndexBoards = map[string]string{"000300": "BK0500"}
