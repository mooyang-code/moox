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
	HTTPClient        *http.Client
	Now               func() time.Time
	RateLimit         marketdata.RateLimitPolicy
	MaxBarsPerRequest int
}

type Provider struct {
	baseURL       string
	klineEndpoint string
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
	return &Provider{baseURL: strings.TrimRight(cfg.BaseURL, "/"), klineEndpoint: cfg.KlineEndpoint, client: cfg.HTTPClient, now: cfg.Now, rateLimit: cfg.RateLimit, maxBars: cfg.MaxBarsPerRequest}
}

func (*Provider) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{ID: "tencent", DisplayName: "Tencent", Hosts: []string{"web.ifzq.gtimg.cn"}}
}

func (p *Provider) KlineSpec() marketdata.KlineSpec {
	return marketdata.KlineSpec{
		Markets: []string{"stock_cn"},
		// The public Tencent m1 endpoint is verified for Shanghai and
		// Shenzhen symbols only. Do not advertise XBSE and let the router
		// choose Sina/EastMoney for Beijing symbols instead.
		Exchanges:     []string{"XSHG", "XSHE"},
		Frequencies:   []string{"1m"},
		CompleteOHLCV: true,
		// Tencent's public m1 payload omits turnover. We derive a conservative
		// CNY estimate from close * shares and mark every normalized row as
		// estimated so consumers do not mistake it for an upstream amount.
		HasAmount:         true,
		MaxBarsPerRequest: p.maxBars,
		TimestampMode:     marketdata.TimestampModeClose,
		// The public m1 endpoint exposes only its latest bounded page; do not
		// accept a longer backfill window that cannot be paged safely.
		History:   marketdata.KlineHistoryCapability{MaxLookback: 24 * time.Hour},
		RateLimit: p.rateLimit,
	}
}

type payload struct {
	Data map[string]struct {
		M1 [][]json.RawMessage `json:"m1"`
	} `json:"data"`
}

func (p *Provider) FetchKlines(ctx context.Context, req marketdata.KlineRequest) ([]marketdata.NormalizedKline, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if req.Frequency != "1m" {
		return nil, fmt.Errorf("%w: tencent only supports 1m", marketdata.ErrUnsupportedFrequency)
	}
	requestLimit := req.Limit
	if !req.StartTime.IsZero() || !req.EndTime.IsZero() {
		// Tencent exposes a latest-page endpoint rather than a start cursor.
		// Fetch its full verified page for historical/gap requests and rely on
		// the common pipeline to discard rows outside the requested interval.
		requestLimit = p.maxBars
	}
	query := url.Values{
		"_var":  {"m1_today"},
		"param": {strings.Join([]string{req.ProviderSymbol, "m1", "", strconv.Itoa(requestLimit)}, ",")},
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
	data := decoded.Data[req.ProviderSymbol].M1
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
		row, err := commonsrc.NormalizeMinuteKline(req.SubjectID, "tencent", req.ProviderSymbol, marketdata.TimestampModeClose, values[0], openValue, highValue, lowValue, closeValue, volumeValue, 0, 100, now, req.RequestID)
		if err != nil {
			return nil, err
		}
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
