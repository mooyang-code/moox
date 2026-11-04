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
	commonsrc "github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn"
)

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
	baseURL       string
	klineEndpoint string
	sourceID      string
	client        *http.Client
	now           func() time.Time
	rateLimit     marketdata.RateLimitPolicy
	maxBars       int
}

func New(cfg Config) *Provider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://ifzq.gtimg.cn"
	}
	if cfg.KlineEndpoint == "" {
		cfg.KlineEndpoint = "/appstock/app/kline/mkline"
	}
	if strings.TrimSpace(cfg.SourceID) == "" {
		cfg.SourceID = "stockcn_http"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 5 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.RateLimit.RequestsPerSecond <= 0 {
		cfg.RateLimit = marketdata.RateLimitPolicy{RequestsPerSecond: 5, Burst: 2, MaxConcurrent: 1, Cooldown: time.Second, RequestTimeout: 5 * time.Second}
	}
	if cfg.MaxBarsPerRequest <= 0 {
		cfg.MaxBarsPerRequest = 320
	}
	return &Provider{baseURL: strings.TrimRight(cfg.BaseURL, "/"), klineEndpoint: cfg.KlineEndpoint, sourceID: strings.ToLower(strings.TrimSpace(cfg.SourceID)), client: cfg.HTTPClient, now: cfg.Now, rateLimit: cfg.RateLimit, maxBars: cfg.MaxBarsPerRequest}
}

func (p *Provider) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{ID: "tencent", SourceID: p.sourceID, DisplayName: "Tencent", Hosts: []string{"web.ifzq.gtimg.cn"}, ProtocolVariant: "http", Transport: "https", Port: 443, Status: marketdata.SourceEnabled}
}

func (p *Provider) KlineSpec() marketdata.KlineSpec {
	return marketdata.KlineSpec{
		Markets: []string{"stockcn"},
		// The public Tencent m1 endpoint is verified for Shanghai and
		// Shenzhen symbols only. Do not advertise XBSE and let the router
		// choose Sina/EastMoney for Beijing symbols instead.
		Exchanges:     []string{"XSHG", "XSHE"},
		Frequencies:   []string{"1m", "5m"},
		CompleteOHLCV: true,
		// 接口不给成交额：按 收盘价 × 成交量 估算写入，并在 amount_quality 标明是估算值。
		HasAmount:         true,
		MaxBarsPerRequest: p.maxBars,
		// 时间标签是收盘时刻（2026-11-04 实测 5m：首根 0935，含 09:25 集合竞价；下午首根 1305）；成交量单位是手（可有小数），乘 100 换成股。
		TimestampMode: marketdata.TimestampModeClose,
		// 接口只给最新一页、没有起点游标：1m 保持 24 小时；5m 一页 320 根约 6.7 个交易日，保守声明 5 个自然日。
		History:            marketdata.KlineHistoryCapability{MaxLookback: 24 * time.Hour},
		HistoryByFrequency: map[string]marketdata.KlineHistoryCapability{"5m": {MaxLookback: 5 * 24 * time.Hour}},
		RateLimit:          p.rateLimit,
	}
}

// payload 的 data.<代码> 下按周期名（m1、m5）给出 K 线。
type payload struct {
	Data map[string]map[string]json.RawMessage `json:"data"`
}

// tencentPeriods 是 K 线接口的周期名。
var tencentPeriods = map[marketdata.Frequency]string{
	marketdata.FrequencyMinute: "m1",
	marketdata.Frequency5Min:   "m5",
}

func (p *Provider) FetchKlines(ctx context.Context, req marketdata.KlineRequest) ([]marketdata.NormalizedKline, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	frequency, _ := req.FrequencyValue() // Validate 已校验
	period, ok := tencentPeriods[frequency]
	if !ok {
		return nil, fmt.Errorf("%w: tencent 不支持 %s", marketdata.ErrUnsupportedFrequency, req.Frequency)
	}
	// 接口只给最新一页、没有起点游标：按起始时间数出要几根，公共写入边界再按请求区间过滤。
	requestLimit := commonsrc.LatestPageLimit(req, p.now().UTC(), p.maxBars)
	query := url.Values{
		"_var":  {period + "_today"},
		"param": {strings.Join([]string{req.ProviderSymbol, period, "", strconv.Itoa(requestLimit)}, ",")},
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+p.klineEndpoint+"?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("User-Agent", "moox-collector/1.0")
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", marketdata.ErrTimeout, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, marketdata.ErrRateLimited
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%w: status=%d", marketdata.ErrHTTPStatus, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: read response body: %v", marketdata.ErrProtocol, err)
	}
	raw := strings.TrimSpace(string(body))
	if index := strings.Index(raw, "="); index >= 0 {
		raw = raw[index+1:]
	}
	raw = strings.TrimSuffix(strings.TrimSpace(raw), ";")
	var decoded payload
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil, fmt.Errorf("%w: %v", marketdata.ErrProtocol, err)
	}
	var data [][]json.RawMessage
	if raw, ok := decoded.Data[req.ProviderSymbol][period]; ok {
		if err := json.Unmarshal(raw, &data); err != nil {
			return nil, fmt.Errorf("%w: tencent %s: %v", marketdata.ErrProtocol, period, err)
		}
	}
	rows := make([]marketdata.NormalizedKline, 0, len(data))
	now := p.now().UTC()
	for _, record := range data {
		if len(record) < 6 {
			return nil, fmt.Errorf("%w: tencent record columns", marketdata.ErrProtocol)
		}
		values := make([]string, 6)
		for index := range values {
			if err := json.Unmarshal(record[index], &values[index]); err != nil {
				return nil, fmt.Errorf("%w: tencent column %d is not a string", marketdata.ErrProtocol, index)
			}
		}
		openValue, err := commonsrc.ParseFloat(values[1])
		if err != nil {
			return nil, err
		}
		closeValue, err := commonsrc.ParseFloat(values[2])
		if err != nil {
			return nil, err
		}
		highValue, err := commonsrc.ParseFloat(values[3])
		if err != nil {
			return nil, err
		}
		lowValue, err := commonsrc.ParseFloat(values[4])
		if err != nil {
			return nil, err
		}
		volumeValue, err := commonsrc.ParseFloat(values[5])
		if err != nil {
			return nil, err
		}
		row, err := commonsrc.NormalizeIntradayKline(req.SubjectID, "tencent", req.ProviderSymbol, frequency, marketdata.TimestampModeClose, values[0], openValue, highValue, lowValue, closeValue, volumeValue, 0, 100, now, req.RequestID)
		if err != nil {
			return nil, err
		}
		row.SourceID = p.sourceID
		row.AmountCNY = row.Close * row.VolumeShares
		row.AmountEstimated = true
		if !now.Before(row.BarEnd) {
			rows = append(rows, row)
		}
	}
	if len(rows) == 0 {
		return nil, marketdata.ErrNoClosedBar
	}
	return rows, nil
}
