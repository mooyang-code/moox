// Package eastmoney 是港股的东方财富 K 线数据源：只取 K 线，不取全部标的、不取指数成分
// （东方财富的标的列表是 A 股的，登记成港股的来源会把 A 股标的当成港股的；恒生指数成分另由腾讯提供）。
// 请求、解析、收盘标签换开盘键是与 A 股共用的代码（见 eastmoneykline），这里只有港股自己的部分。
package eastmoney

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/markets/stockhk"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/eastmoneykline"
)

const (
	providerID = "eastmoney"
	// DefaultSourceID 是行情源目录里登记的 source_id，也是路由文件里写的那一个。
	DefaultSourceID = "stockhk_http"
	// secidPrefix 是港股在东方财富的市场编号：00700 的 secid 是 116.00700。
	secidPrefix = "116."
	// volumeMultiplier：成交量单位已经是股，不用换算（2026-11-12 实测；成交额单位是港元）。
	volumeMultiplier = 1
	// maxLookback 是 5m 的回看能力，按自然时长声明：2026-11-12 实测最早到 10-12，共 1496 根、约 23 个交易日，
	// beg 写得再早也只返回这么多。
	maxLookback = 28 * 24 * time.Hour
	// httpTimeout 是 HTTP 客户端的超时上限；单次请求实际的超时是限速配置里的 request_timeout，随请求的 ctx 生效。
	httpTimeout = 30 * time.Second
)

// klineFrequencies 是支持的周期：只有 5m。
var klineFrequencies = []string{"5m"}

// hongKong 是 K 线时间标签的时区（与北京时间相同）。标签是收盘时刻：每天 66 根，首根 09:35、末根 16:00，
// 上午最后一根 12:00、下午第一根 13:05。
var hongKong = mustLoadLocation("Asia/Hong_Kong")

func mustLoadLocation(name string) *time.Location {
	location, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return location
}

// Config 里的地址、source_id、限速、单次根数来自路由文件与数据源配置文件。
type Config struct {
	BaseURL           string
	KlineEndpoint     string
	SourceID          string
	HTTPClient        *http.Client
	Now               func() time.Time
	RateLimit         marketdata.RateLimitPolicy
	MaxBarsPerRequest int
}

type Provider struct {
	klines    *eastmoneykline.Client
	rateLimit marketdata.RateLimitPolicy
}

func New(cfg Config) *Provider {
	if strings.TrimSpace(cfg.SourceID) == "" {
		cfg.SourceID = DefaultSourceID
	}
	if cfg.RateLimit.RequestsPerSecond <= 0 {
		cfg.RateLimit = eastmoneykline.DefaultRateLimit()
	}
	if cfg.HTTPClient == nil {
		// 与 A 股的东方财富相同：自己建一个客户端，用 Go 默认的连接池。
		cfg.HTTPClient = &http.Client{Timeout: httpTimeout}
	}
	klines := eastmoneykline.New(eastmoneykline.Config{
		BaseURL: cfg.BaseURL, Endpoint: cfg.KlineEndpoint, ProviderID: providerID, SourceID: cfg.SourceID,
		HTTPClient: cfg.HTTPClient, Now: cfg.Now, MaxBarsPerRequest: cfg.MaxBarsPerRequest,
		Frequencies: klineFrequencies, SecID: secID, VolumeMultiplier: volumeMultiplier, Location: hongKong,
	})
	return &Provider{klines: klines, rateLimit: cfg.RateLimit}
}

// secID 把标的 ID 换成东方财富的 secid：00700.XHKG → 116.00700。代码与交易所的校验用港股的标的规则。
func secID(subjectID string) (string, error) {
	subject, err := stockhk.Rules{}.ParseSubject(marketdata.InstrumentEquity, subjectID)
	if err != nil {
		return "", err
	}
	return secidPrefix + subject.Code, nil
}

func (p *Provider) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{ID: providerID, SourceID: p.klines.SourceID(), DisplayName: "EastMoney HK", Hosts: []string{eastmoneykline.Host}, ProtocolVariant: "http", Transport: "https", Port: 443, Status: marketdata.SourceEnabled}
}

func (p *Provider) KlineSpec() marketdata.KlineSpec {
	return marketdata.KlineSpec{
		Markets:           []string{stockhk.MarketID},
		Exchanges:         []string{stockhk.Exchange},
		Frequencies:       slices.Clone(klineFrequencies),
		CompleteOHLCV:     true,
		HasAmount:         true,
		MaxBarsPerRequest: p.klines.MaxBarsPerRequest(),
		TimestampMode:     marketdata.TimestampModeClose,
		History:           marketdata.KlineHistoryCapability{MaxLookback: maxLookback},
		RateLimit:         p.rateLimit,
	}
}

// FetchKlines 取 K 线：请求、解析、收盘标签换开盘键都在共用代码里（见 eastmoneykline）。
func (p *Provider) FetchKlines(ctx context.Context, req marketdata.KlineRequest) ([]marketdata.NormalizedKline, error) {
	return p.klines.FetchKlines(ctx, req)
}
