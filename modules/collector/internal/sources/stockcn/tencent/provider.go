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
		cfg.BaseURL = "https://web.ifzq.gtimg.cn"
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
	return marketdata.ProviderDescriptor{ID: "tencent", DisplayName: "Tencent", Hosts: []string{"web.ifzq.gtimg.cn"}}
}

func (*Provider) KlineSpec() marketdata.KlineSpec {
	return marketdata.KlineSpec{
		Markets:           []string{"stock_cn"},
		Exchanges:         []string{"XSHG", "XSHE", "XBSE"},
		Frequencies:       []string{"1m"},
		CompleteOHLCV:     true,
		HasAmount:         true,
		MaxBarsPerRequest: 320,
		TimestampMode:     marketdata.TimestampModeClose,
		RateLimit: marketdata.RateLimitPolicy{
			RequestsPerSecond: 5,
			Burst:             2,
			MaxConcurrent:     1,
			Cooldown:          time.Second,
			RequestTimeout:    5 * time.Second,
		},
	}
}

type payload struct {
	Data map[string]map[string][][]string `json:"data"`
}

func (p *Provider) FetchKlines(ctx context.Context, req marketdata.KlineRequest) ([]marketdata.NormalizedKline, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if req.Frequency != "1m" {
		return nil, fmt.Errorf("%w: tencent only supports 1m", marketdata.ErrUnsupportedFrequency)
	}
	query := url.Values{
		"_var":  {"m1_today"},
		"param": {strings.Join([]string{req.ProviderSymbol, "m1", "", strconv.Itoa(req.Limit)}, ",")},
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/appstock/app/kline/mkline?"+query.Encode(), nil)
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
	raw := strings.TrimSpace(string(body))
	if index := strings.Index(raw, "="); index >= 0 {
		raw = raw[index+1:]
	}
	raw = strings.TrimSuffix(strings.TrimSpace(raw), ";")
	var decoded payload
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil, fmt.Errorf("%w: %v", marketdata.ErrProtocol, err)
	}
	data := decoded.Data[req.ProviderSymbol]["m1"]
	rows := make([]marketdata.NormalizedKline, 0, len(data))
	now := p.now().UTC()
	for _, record := range data {
		if len(record) < 6 {
			return nil, fmt.Errorf("%w: tencent record columns", marketdata.ErrProtocol)
		}
		openValue, err := commonsrc.ParseFloat(record[1])
		if err != nil {
			return nil, err
		}
		closeValue, err := commonsrc.ParseFloat(record[2])
		if err != nil {
			return nil, err
		}
		highValue, err := commonsrc.ParseFloat(record[3])
		if err != nil {
			return nil, err
		}
		lowValue, err := commonsrc.ParseFloat(record[4])
		if err != nil {
			return nil, err
		}
		volumeValue, err := commonsrc.ParseFloat(record[5])
		if err != nil {
			return nil, err
		}
		amountValue := 0.0
		if len(record) > 6 && strings.TrimSpace(record[6]) != "" {
			amountValue, err = commonsrc.ParseFloat(record[6])
			if err != nil {
				return nil, err
			}
		}
		row, err := commonsrc.NormalizeMinuteKline(req.SubjectID, "tencent", req.ProviderSymbol, marketdata.TimestampModeClose, record[0], openValue, highValue, lowValue, closeValue, volumeValue, amountValue, 100, now, req.RequestID)
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
