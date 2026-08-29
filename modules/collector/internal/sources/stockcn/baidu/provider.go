package baidu

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
		cfg.BaseURL = "https://finance.pae.baidu.com"
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
	return marketdata.ProviderDescriptor{ID: "baidu", DisplayName: "Baidu", Hosts: []string{"finance.pae.baidu.com"}}
}

func (*Provider) ShadowSpec() marketdata.KlineSpec {
	return marketdata.KlineSpec{
		Markets:           []string{"stock_cn"},
		Exchanges:         []string{"XSHG", "XSHE", "XBSE"},
		Frequencies:       []string{"1m"},
		CompleteOHLCV:     false,
		HasAmount:         true,
		MaxBarsPerRequest: 240,
		TimestampMode:     marketdata.TimestampModeOpen,
		RateLimit: marketdata.RateLimitPolicy{
			RequestsPerSecond: 2,
			Burst:             1,
			MaxConcurrent:     1,
			Cooldown:          time.Second,
			RequestTimeout:    5 * time.Second,
		},
	}
}

type shadowResponse struct {
	Result []struct {
		Time   string  `json:"time"`
		Price  float64 `json:"price"`
		Volume float64 `json:"volume"`
		Amount float64 `json:"amount"`
	} `json:"Result"`
}

func (p *Provider) FetchShadowKlines(ctx context.Context, req marketdata.KlineRequest) ([]commonsrc.ShadowPoint, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	query := url.Values{
		"code": {req.ProviderSymbol},
		"pn":   {fmt.Sprintf("%d", req.Limit)},
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/api/quotation_minute_ab?"+query.Encode(), nil)
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
	var payload shadowResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("%w: %v", marketdata.ErrProtocol, err)
	}
	points := make([]commonsrc.ShadowPoint, 0, len(payload.Result))
	for _, result := range payload.Result {
		barStart, err := time.Parse(time.RFC3339, result.Time)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", marketdata.ErrProtocol, err)
		}
		points = append(points, commonsrc.ShadowPoint{
			SubjectID:      req.SubjectID,
			ProviderID:     "baidu",
			ProviderSymbol: req.ProviderSymbol,
			BarStart:       barStart.UTC(),
			Price:          result.Price,
			VolumeShares:   result.Volume,
			AmountCNY:      result.Amount,
			FetchedAt:      p.now().UTC(),
			RequestID:      req.RequestID,
		})
	}
	return points, nil
}
