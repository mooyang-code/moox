package eastmoney

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	commonsrc "github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn"
)

type Config struct {
	BaseURL    string
	HTTPClient *http.Client
	Now        func() time.Time
}

type Provider struct {
	baseURL string
	client  *http.Client
	now     func() time.Time
}

func New(cfg Config) *Provider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://push2his.eastmoney.com"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 5 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Provider{baseURL: strings.TrimRight(cfg.BaseURL, "/"), client: cfg.HTTPClient, now: cfg.Now}
}

func (*Provider) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{ID: "eastmoney", DisplayName: "EastMoney", Hosts: []string{"push2his.eastmoney.com"}}
}

func (*Provider) KlineSpec() marketdata.KlineSpec {
	return marketdata.KlineSpec{
		Markets:           []string{"stock_cn"},
		Exchanges:         []string{"XSHG", "XSHE", "XBSE"},
		Frequencies:       []string{"1m"},
		CompleteOHLCV:     true,
		HasAmount:         true,
		MaxBarsPerRequest: 1000,
		TimestampMode:     marketdata.TimestampModeOpen,
		RateLimit: marketdata.RateLimitPolicy{
			RequestsPerSecond: 5,
			Burst:             2,
			MaxConcurrent:     1,
			Cooldown:          time.Second,
			RequestTimeout:    5 * time.Second,
		},
	}
}

type eastMoneyResponse struct {
	Data struct {
		Klines []string `json:"klines"`
	} `json:"data"`
}

func (p *Provider) FetchKlines(ctx context.Context, req marketdata.KlineRequest) ([]marketdata.NormalizedKline, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if req.Frequency != "1m" {
		return nil, fmt.Errorf("%w: eastmoney only supports 1m", marketdata.ErrUnsupportedFrequency)
	}
	secid, err := commonsrc.EastMoneySecID(req.SubjectID)
	if err != nil {
		return nil, err
	}
	query := url.Values{
		"secid": {secid},
		"klt":   {"1"},
		"fqt":   {"0"},
		"lmt":   {fmt.Sprintf("%d", req.Limit)},
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/api/qt/stock/kline/get?"+query.Encode(), nil)
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
		return nil, err
	}
	var payload eastMoneyResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("%w: %v", marketdata.ErrProtocol, err)
	}
	rows := make([]marketdata.NormalizedKline, 0, len(payload.Data.Klines))
	now := p.now().UTC()
	for _, line := range payload.Data.Klines {
		fields := strings.Split(line, ",")
		if len(fields) < 7 {
			return nil, fmt.Errorf("%w: eastmoney kline columns", marketdata.ErrProtocol)
		}
		openValue, err := commonsrc.ParseFloat(fields[1])
		if err != nil {
			return nil, err
		}
		closeValue, err := commonsrc.ParseFloat(fields[2])
		if err != nil {
			return nil, err
		}
		highValue, err := commonsrc.ParseFloat(fields[3])
		if err != nil {
			return nil, err
		}
		lowValue, err := commonsrc.ParseFloat(fields[4])
		if err != nil {
			return nil, err
		}
		volumeValue, err := commonsrc.ParseFloat(fields[5])
		if err != nil {
			return nil, err
		}
		amountValue, err := commonsrc.ParseFloat(fields[6])
		if err != nil {
			return nil, err
		}
		row, err := commonsrc.NormalizeMinuteKline(req.SubjectID, "eastmoney", req.ProviderSymbol, marketdata.TimestampModeOpen, fields[0], openValue, highValue, lowValue, closeValue, volumeValue, amountValue, 100, now, req.RequestID)
		if err != nil {
			return nil, err
		}
		if !now.Before(row.BarEnd) {
			rows = append(rows, row)
		}
	}
	if len(rows) == 0 {
		return nil, marketdata.ErrNoClosedBar
	}
	return rows, nil
}
