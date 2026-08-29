package sina

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	commonsrc "github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn"
)

type Config struct {
	BaseURL                  string
	InstrumentBaseURL        string
	HTTPClient               *http.Client
	Now                      func() time.Time
	InstrumentRequestTimeout time.Duration
}

type Provider struct {
	baseURL             string
	instrumentBaseURL   string
	client              *http.Client
	now                 func() time.Time
	instrumentTimeout   time.Duration
	instrumentGuardOnce sync.Once
	instrumentGuard     *marketdata.FeedGuard
	instrumentGuardErr  error
}

func New(cfg Config) *Provider {
	if cfg.InstrumentBaseURL == "" && cfg.BaseURL != "" {
		cfg.InstrumentBaseURL = cfg.BaseURL
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://quotes.sina.cn"
	}
	if cfg.InstrumentBaseURL == "" {
		cfg.InstrumentBaseURL = "https://vip.stock.finance.sina.com.cn"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 5 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.InstrumentRequestTimeout <= 0 {
		cfg.InstrumentRequestTimeout = 5 * time.Second
	}
	return &Provider{baseURL: strings.TrimRight(cfg.BaseURL, "/"), instrumentBaseURL: strings.TrimRight(cfg.InstrumentBaseURL, "/"), client: cfg.HTTPClient, now: cfg.Now, instrumentTimeout: cfg.InstrumentRequestTimeout}
}

func (*Provider) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{ID: "sina", DisplayName: "Sina", Hosts: []string{"quotes.sina.cn", "vip.stock.finance.sina.com.cn"}}
}

func (*Provider) KlineSpec() marketdata.KlineSpec {
	return marketdata.KlineSpec{
		Markets:           []string{"stock_cn"},
		Exchanges:         []string{"XSHG", "XSHE", "XBSE"},
		Frequencies:       []string{"1m"},
		CompleteOHLCV:     true,
		HasAmount:         true,
		MaxBarsPerRequest: 1023,
		TimestampMode:     marketdata.TimestampModeClose,
		History:           marketdata.KlineHistoryCapability{MaxLookback: 5 * 24 * time.Hour},
		RateLimit: marketdata.RateLimitPolicy{
			RequestsPerSecond: 5,
			Burst:             2,
			MaxConcurrent:     1,
			Cooldown:          time.Second,
			RequestTimeout:    5 * time.Second,
		},
	}
}

func (p *Provider) InstrumentSpec() marketdata.InstrumentSpec {
	return marketdata.InstrumentSpec{
		Markets:      []string{"stock_cn"},
		Exchanges:    []string{"XSHG", "XSHE", "XBSE"},
		FullSnapshot: true,
		PageSize:     100,
		RateLimit: marketdata.RateLimitPolicy{
			RequestsPerSecond: 5,
			Burst:             2,
			MaxConcurrent:     1,
			Cooldown:          time.Second,
			RequestTimeout:    p.instrumentTimeout,
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
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/cn/api/jsonp_v2.php/var%20moox_kline=/CN_MarketDataService.getKLineData?"+query.Encode(), nil)
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
	if index := strings.Index(raw, "var moox_kline="); index >= 0 {
		raw = raw[index+len("var moox_kline="):]
	}
	raw = strings.TrimSuffix(strings.TrimSpace(raw), ";")
	raw = strings.TrimPrefix(raw, "(")
	raw = strings.TrimSuffix(raw, ")")
	var payload []sinaBar
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
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
		row, err := commonsrc.NormalizeMinuteKline(req.SubjectID, "sina", req.ProviderSymbol, marketdata.TimestampModeClose, record.Day, openValue, highValue, lowValue, closeValue, volumeValue, amountValue, 1, now, req.RequestID)
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
	p.instrumentGuardOnce.Do(func() {
		p.instrumentGuard, p.instrumentGuardErr = marketdata.NewFeedGuard(spec.RateLimit, nil, nil)
	})
	if p.instrumentGuardErr != nil {
		return marketdata.InstrumentSnapshot{}, p.instrumentGuardErr
	}
	declaredPages := 0
	previousPageSize := 0
	for page := 1; ; page++ {
		query := url.Values{
			"page":   {strconv.Itoa(page)},
			"num":    {strconv.Itoa(spec.PageSize)},
			"sort":   {"symbol"},
			"asc":    {"1"},
			"node":   {"hs_a"},
			"_s_r_a": {"page"},
		}
		var body []byte
		if err := p.instrumentGuard.Do(ctx, func(pageCtx context.Context) error {
			httpReq, requestErr := http.NewRequestWithContext(pageCtx, http.MethodGet, p.instrumentBaseURL+"/quotes_service/api/json_v2.php/Market_Center.getHQNodeData?"+query.Encode(), nil)
			if requestErr != nil {
				return requestErr
			}
			httpReq.Header.Set("User-Agent", "moox-collector/1.0")
			resp, requestErr := p.client.Do(httpReq)
			if requestErr != nil {
				return fmt.Errorf("%w: %v", marketdata.ErrTimeout, requestErr)
			}
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusTooManyRequests {
				return marketdata.ErrRateLimited
			}
			if resp.StatusCode >= 400 {
				return fmt.Errorf("%w: status=%d", marketdata.ErrHTTPStatus, resp.StatusCode)
			}
			body, requestErr = io.ReadAll(io.LimitReader(resp.Body, 4<<20))
			return requestErr
		}); err != nil {
			return marketdata.InstrumentSnapshot{}, err
		}
		trimmedBody := bytes.TrimSpace(body)
		if len(trimmedBody) == 0 {
			return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: sina instrument page %d returned an empty body", marketdata.ErrProtocol, page)
		}
		var direct []map[string]any
		var items []map[string]any
		totalPages := 0
		switch trimmedBody[0] {
		case '[':
			if err := json.Unmarshal(trimmedBody, &direct); err != nil {
				return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: %v", marketdata.ErrProtocol, err)
			}
			items = direct
		case '{':
			var payload map[string]any
			if err := json.Unmarshal(trimmedBody, &payload); err != nil {
				return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: %v", marketdata.ErrProtocol, err)
			}
			data := commonsrc.ObjectAt(payload, "data")
			if data == nil {
				data = payload
			}
			if _, ok := firstPresent(data, "list", "items", "diff"); !ok {
				return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: sina instrument page %d has no recognized item list", marketdata.ErrProtocol, page)
			}
			items = commonsrc.ItemSlice(data, "list", "items", "diff")
			totalPages = commonsrc.PageLimit(data, spec.PageSize)
		default:
			return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: sina instrument page %d has unsupported response shape", marketdata.ErrProtocol, page)
		}
		if totalPages > 0 {
			if declaredPages == 0 {
				declaredPages = totalPages
			} else if totalPages != declaredPages {
				return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: sina instrument page count changed from %d to %d on page %d", marketdata.ErrProtocol, declaredPages, totalPages, page)
			}
		}
		if len(items) == 0 {
			if declaredPages > 0 || page == 1 || previousPageSize != spec.PageSize {
				return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: sina instrument page %d is empty without terminal evidence", marketdata.ErrProtocol, page)
			}
			builder.NextPage()
			break
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
		previousPageSize = len(items)
		if declaredPages > 0 {
			if page >= declaredPages {
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

func firstPresent(values map[string]any, keys ...string) (any, bool) {
	for _, key := range keys {
		if value, ok := values[key]; ok {
			return value, true
		}
	}
	return nil, false
}
