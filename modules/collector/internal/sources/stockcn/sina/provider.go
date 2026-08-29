package sina

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
		cfg.BaseURL = "https://quotes.sina.cn"
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
	return marketdata.ProviderDescriptor{ID: "sina", DisplayName: "Sina", Hosts: []string{"quotes.sina.cn"}}
}

func (*Provider) KlineSpec() marketdata.KlineSpec {
	return marketdata.KlineSpec{
		Markets:           []string{"stock_cn"},
		Exchanges:         []string{"XSHG", "XSHE", "XBSE"},
		Frequencies:       []string{"1m"},
		CompleteOHLCV:     true,
		HasAmount:         true,
		MaxBarsPerRequest: 1023,
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

type sinaBar struct {
	Day    string `json:"day"`
	Open   string `json:"open"`
	High   string `json:"high"`
	Low    string `json:"low"`
	Close  string `json:"close"`
	Volume string `json:"volume"`
	Amount string `json:"amount"`
}

func (p *Provider) FetchKlines(ctx context.Context, req marketdata.KlineRequest) ([]marketdata.NormalizedKline, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if req.Frequency != "1m" {
		return nil, fmt.Errorf("%w: sina only supports 1m", marketdata.ErrUnsupportedFrequency)
	}
	query := url.Values{
		"symbol":  {req.ProviderSymbol},
		"scale":   {"1"},
		"ma":      {"no"},
		"datalen": {fmt.Sprintf("%d", req.Limit)},
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/CN_MarketDataService.getKLineData?"+query.Encode(), nil)
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
	var payload []sinaBar
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("%w: %v", marketdata.ErrProtocol, err)
	}
	rows := make([]marketdata.NormalizedKline, 0, len(payload))
	now := p.now().UTC()
	for _, record := range payload {
		openValue, err := commonsrc.ParseFloat(record.Open)
		if err != nil {
			return nil, err
		}
		highValue, err := commonsrc.ParseFloat(record.High)
		if err != nil {
			return nil, err
		}
		lowValue, err := commonsrc.ParseFloat(record.Low)
		if err != nil {
			return nil, err
		}
		closeValue, err := commonsrc.ParseFloat(record.Close)
		if err != nil {
			return nil, err
		}
		volumeValue, err := commonsrc.ParseFloat(record.Volume)
		if err != nil {
			return nil, err
		}
		amountValue, err := commonsrc.ParseFloat(record.Amount)
		if err != nil {
			return nil, err
		}
		row, err := commonsrc.NormalizeMinuteKline(req.SubjectID, "sina", req.ProviderSymbol, marketdata.TimestampModeOpen, record.Day, openValue, highValue, lowValue, closeValue, volumeValue, amountValue, 1, now, req.RequestID)
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
