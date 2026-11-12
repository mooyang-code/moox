// Package eastmoney 是美股的东方财富 K 线数据源：只取 K 线，不取全部标的、不取指数成分
// （东方财富的美股列表有一万多个代码，含大量 ETF 与退市股，登记成“取全部标的”会把它们都当成标的；成分另有来源）。
// 请求、解析、收盘标签换开盘键是与 A 股、港股共用的代码（见 eastmoneykline），这里只有美股自己的部分：
// secid 的写法、成交量单位、时区、支持的周期，以及日期区间规则（与 A 股、港股不同）。
package eastmoney

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/eastmoneykline"
)

const (
	providerID = "eastmoney"
	// DefaultSourceID 是行情源目录里登记的 source_id，也是路由文件里写的那一个。
	DefaultSourceID = "stockus_http"

	// marketID 是美股市场的空间 ID，交易所是标的 ID 后缀的 MIC。
	marketID       = "stockus"
	exchangeNasdaq = "XNAS"
	exchangeNYSE   = "XNYS"
	exchangeOther  = "XASE"

	// volumeMultiplier：成交量单位已经是股，不用换算（2026-11-12 实测；成交额单位是美元）。
	volumeMultiplier = 1
	// maxLookback 是 5m 的回看能力，按自然时长声明：2026-11-12 实测全量 1412 根，是 18 个交易日，
	// 最早到 10-19 21:35，按交易日滚动，beg 写得再早也只返回这么多。
	maxLookback = 21 * 24 * time.Hour
	// httpTimeout 是 HTTP 客户端的超时上限；单次请求实际的超时是限速配置里的 request_timeout，随请求的 ctx 生效。
	httpTimeout = 30 * time.Second
)

// secidPrefix 是东方财富的美股市场编号：105 纳斯达克、106 纽交所、107 其他（含 ETF 与 Cboe 上市的代码）。
// 键是标的 ID 的交易所后缀，东方财富的 107 不是美交所一家，登记时一律写成 XASE。
var secidPrefix = map[string]string{
	exchangeNasdaq: "105.",
	exchangeNYSE:   "106.",
	exchangeOther:  "107.",
}

// klineFrequencies 是支持的周期：只有 5m。
var klineFrequencies = []string{"5m"}

// beijing 是 K 线时间标签的时区：东方财富给的美股标签是北京时间的收盘时刻。冬令时每天首根 22:35、末根次日 05:00，
// 夏令时首根 21:35、末根次日 04:00，半日市 13:00 收市。
var beijing = mustLoadLocation("Asia/Shanghai")

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
		// 与 A 股、港股的东方财富相同：自己建一个客户端，用 Go 默认的连接池。
		cfg.HTTPClient = &http.Client{Timeout: httpTimeout}
	}
	klines := eastmoneykline.New(eastmoneykline.Config{
		BaseURL: cfg.BaseURL, Endpoint: cfg.KlineEndpoint, ProviderID: providerID, SourceID: cfg.SourceID,
		HTTPClient: cfg.HTTPClient, Now: cfg.Now, MaxBarsPerRequest: cfg.MaxBarsPerRequest,
		Frequencies: klineFrequencies, SecID: secID, DateRange: dateRange, VolumeMultiplier: volumeMultiplier, Location: beijing,
	})
	return &Provider{klines: klines, rateLimit: cfg.RateLimit}
}

// secID 按请求的交易所与代码换成东方财富的 secid：XNAS 的 AAPL → 105.AAPL，XNYS 的 BRK.B → 106.BRK_B。
// 代码里的点要换成下划线，写成点返回空列表（2026-11-12 实测）。交易所不在三者之中报错。
func secID(req marketdata.KlineRequest) (string, error) {
	exchange := strings.ToUpper(strings.TrimSpace(string(req.ExchangeID)))
	prefix, ok := secidPrefix[exchange]
	if !ok {
		return "", fmt.Errorf("%w: 美股标的 %q 的交易所 %q 必须是 %s、%s、%s 之一", marketdata.ErrUnsupportedSymbol, req.SubjectID, req.ExchangeID, exchangeNasdaq, exchangeNYSE, exchangeOther)
	}
	code := strings.ToUpper(strings.TrimSpace(req.ProviderSymbol))
	if code == "" {
		return "", fmt.Errorf("%w: 美股标的 %q 的代码为空", marketdata.ErrUnsupportedSymbol, req.SubjectID)
	}
	return prefix + strings.ReplaceAll(code, ".", "_"), nil
}

func (p *Provider) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{ID: providerID, SourceID: p.klines.SourceID(), DisplayName: "EastMoney US", Hosts: []string{eastmoneykline.Host}, ProtocolVariant: "http", Transport: "https", Port: 443, Status: marketdata.SourceEnabled}
}

func (p *Provider) KlineSpec() marketdata.KlineSpec {
	return marketdata.KlineSpec{
		Markets:           []string{marketID},
		Exchanges:         []string{exchangeNasdaq, exchangeNYSE, exchangeOther},
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
