package baidu

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
	SourceID   string
	HTTPClient *http.Client
	Now        func() time.Time
	RateLimit  marketdata.RateLimitPolicy
}

type Provider struct {
	baseURL   string
	sourceID  string
	client    *http.Client
	now       func() time.Time
	rateLimit marketdata.RateLimitPolicy
}

func New(cfg Config) *Provider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://finance.pae.baidu.com"
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
		cfg.RateLimit = marketdata.RateLimitPolicy{RequestsPerSecond: 2, Burst: 1, MaxConcurrent: 1, Cooldown: time.Second, RequestTimeout: 5 * time.Second}
	}
	return &Provider{baseURL: strings.TrimRight(cfg.BaseURL, "/"), sourceID: strings.ToLower(strings.TrimSpace(cfg.SourceID)), client: cfg.HTTPClient, now: cfg.Now, rateLimit: cfg.RateLimit}
}

func (p *Provider) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{ID: "baidu", SourceID: p.sourceID, DisplayName: "Baidu", Hosts: []string{"finance.pae.baidu.com"}, ProtocolVariant: "http", Transport: "https", Port: 443, Status: marketdata.SourceShadow}
}

func (p *Provider) ShadowSpec() marketdata.KlineSpec {
	return marketdata.KlineSpec{
		Markets:           []string{"stockcn"},
		Exchanges:         []string{"XSHG", "XSHE", "XBSE"},
		Frequencies:       []string{"1m"},
		CompleteOHLCV:     false,
		HasAmount:         true,
		MaxBarsPerRequest: 240,
		TimestampMode:     marketdata.TimestampModeOpen,
		RateLimit:         p.rateLimit,
	}
}

func (p *Provider) InstrumentSpec() marketdata.InstrumentSpec {
	return marketdata.InstrumentSpec{
		Markets:      []string{"stockcn"},
		Exchanges:    []string{"XSHG", "XSHE", "XBSE"},
		FullSnapshot: true,
		PageSize:     200,
		RateLimit:    p.rateLimit,
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
		return nil, fmt.Errorf("%w: read response body: %v", marketdata.ErrProtocol, err)
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

func (p *Provider) FetchInstrumentSnapshot(ctx context.Context, req marketdata.InstrumentRequest) (marketdata.InstrumentSnapshot, error) {
	marketID := strings.TrimSpace(string(req.MarketID))
	if marketID == "" {
		marketID = "stockcn"
	}
	if marketID != "stockcn" {
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
			"pn": {strconv.Itoa(page)},
			"rn": {strconv.Itoa(spec.PageSize)},
		}
		var body []byte
		if err := func(pageCtx context.Context) error {
			httpReq, requestErr := http.NewRequestWithContext(pageCtx, http.MethodGet, p.baseURL+"/api/getmarketrank?"+query.Encode(), nil)
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
			if requestErr != nil {
				return fmt.Errorf("%w: read response body: %v", marketdata.ErrProtocol, requestErr)
			}
			return nil
		}(ctx); err != nil {
			return marketdata.InstrumentSnapshot{}, err
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: %v", marketdata.ErrProtocol, err)
		}
		data := commonsrc.ObjectAt(payload, "data")
		if data == nil {
			data = payload
		}
		items := commonsrc.ItemSlice(data, "Result", "result", "list", "items")
		if len(items) == 0 {
			return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: baidu instrument page %d is empty", marketdata.ErrProtocol, page)
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
