package sina

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

func (*Provider) InstrumentSpec() marketdata.InstrumentSpec {
	return marketdata.InstrumentSpec{
		Markets:      []string{"stock_cn"},
		Exchanges:    []string{"XSHG", "XSHE", "XBSE"},
		FullSnapshot: true,
		PageSize:     500,
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

func (p *Provider) FetchInstrumentSnapshot(ctx context.Context, req marketdata.InstrumentRequest) (marketdata.InstrumentSnapshot, error) {
	marketID := strings.TrimSpace(string(req.MarketID))
	if marketID == "" {
		marketID = "stock_cn"
	}
	if marketID != "stock_cn" {
		return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: market_id %q is unsupported", marketdata.ErrInvalidRequest, req.MarketID)
	}
	spec := p.InstrumentSpec()
	fetchedAt := req.SnapshotAt.UTC()
	if req.SnapshotAt.IsZero() {
		fetchedAt = p.now().UTC()
	}
	builder := commonsrc.NewInstrumentSnapshotBuilder(p.Descriptor().ID, marketID, fetchedAt)
	for page := 1; ; page++ {
		query := url.Values{
			"page": {strconv.Itoa(page)},
			"num":  {strconv.Itoa(spec.PageSize)},
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/api/Market_Center.getHQNodeData?"+query.Encode(), nil)
		if err != nil {
			return marketdata.InstrumentSnapshot{}, err
		}
		httpReq.Header.Set("User-Agent", "moox-collector/1.0")
		resp, err := p.client.Do(httpReq)
		if err != nil {
			return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: %v", marketdata.ErrTimeout, err)
		}
		body, readErr := func() ([]byte, error) {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusTooManyRequests {
				return nil, marketdata.ErrRateLimited
			}
			if resp.StatusCode >= 400 {
				return nil, fmt.Errorf("%w: status=%d", marketdata.ErrHTTPStatus, resp.StatusCode)
			}
			return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		}()
		if readErr != nil {
			return marketdata.InstrumentSnapshot{}, readErr
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: %v", marketdata.ErrProtocol, err)
		}
		data := commonsrc.ObjectAt(payload, "data")
		if data == nil {
			data = payload
		}
		items := commonsrc.ItemSlice(data, "list", "items", "diff")
		if len(items) == 0 {
			return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: sina instrument page %d is empty", marketdata.ErrProtocol, page)
		}
		for _, item := range items {
			instrument, err := commonsrc.InstrumentFromFields(
				commonsrc.StringField(item, "code", "f12", "symbol"),
				commonsrc.StringField(item, "symbol", "provider_symbol"),
				commonsrc.StringField(item, "market", "f13", "exchange"),
				commonsrc.StringField(item, "name", "f14", "sname"),
				commonsrc.StringField(item, "status", "state", "trade_status"),
			)
			if err != nil {
				return marketdata.InstrumentSnapshot{}, err
			}
			if err := builder.Add(instrument); err != nil {
				return marketdata.InstrumentSnapshot{}, err
			}
		}
		builder.NextPage()
		if totalPages := commonsrc.PageLimit(data, spec.PageSize); totalPages > 0 {
			if page >= totalPages {
				break
			}
			continue
		}
		if hasMore, ok := commonsrc.BoolField(data, "hasnext", "has_next", "hasMore", "has_more"); ok {
			if !hasMore {
				break
			}
			continue
		}
		if len(items) < spec.PageSize {
			break
		}
	}
	return builder.Snapshot()
}
