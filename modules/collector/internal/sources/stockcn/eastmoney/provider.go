package eastmoney

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	commonsrc "github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn"
)

type Config struct {
	BaseURL                  string
	KlineEndpoint            string
	SourceID                 string
	HTTPClient               *http.Client
	Now                      func() time.Time
	InstrumentRequestTimeout time.Duration
	RateLimit                marketdata.RateLimitPolicy
	MaxBarsPerRequest        int
}

type Provider struct {
	baseURL             string
	klineEndpoint       string
	sourceID            string
	client              *http.Client
	now                 func() time.Time
	rateLimit           marketdata.RateLimitPolicy
	instrumentRateLimit marketdata.RateLimitPolicy
	maxBars             int
}

func New(cfg Config) *Provider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://push2.eastmoney.com"
	}
	if cfg.KlineEndpoint == "" {
		cfg.KlineEndpoint = "/api/qt/stock/kline/get"
	}
	if strings.TrimSpace(cfg.SourceID) == "" {
		cfg.SourceID = "stockcn_http"
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.RateLimit.RequestsPerSecond <= 0 {
		cfg.RateLimit = marketdata.RateLimitPolicy{RequestsPerSecond: 5, Burst: 2, MaxConcurrent: 1, Cooldown: time.Second, RequestTimeout: 5 * time.Second}
	}
	if cfg.InstrumentRequestTimeout <= 0 {
		cfg.InstrumentRequestTimeout = 15 * time.Second
	}
	if cfg.HTTPClient == nil {
		// Keep the HTTP transport at least as patient as the instrument page
		// guard; otherwise a configured 15-second guard is silently truncated.
		cfg.HTTPClient = &http.Client{Timeout: cfg.InstrumentRequestTimeout}
	}
	if cfg.MaxBarsPerRequest <= 0 {
		cfg.MaxBarsPerRequest = 1205
	}
	instrumentRateLimit := cfg.RateLimit
	instrumentRateLimit.RequestTimeout = cfg.InstrumentRequestTimeout
	return &Provider{baseURL: strings.TrimRight(cfg.BaseURL, "/"), klineEndpoint: cfg.KlineEndpoint, sourceID: strings.ToLower(strings.TrimSpace(cfg.SourceID)), client: cfg.HTTPClient, now: cfg.Now, rateLimit: cfg.RateLimit, instrumentRateLimit: instrumentRateLimit, maxBars: cfg.MaxBarsPerRequest}
}

func (p *Provider) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{ID: "eastmoney", SourceID: p.sourceID, DisplayName: "EastMoney", Hosts: []string{"push2.eastmoney.com"}, ProtocolVariant: "http", Transport: "https", Port: 443, Status: marketdata.SourceEnabled}
}

func (p *Provider) KlineSpec() marketdata.KlineSpec {
	return marketdata.KlineSpec{
		Markets:           []string{"stockcn"},
		Exchanges:         []string{"XSHG", "XSHE", "XBSE"},
		Frequencies:       []string{"1m"},
		CompleteOHLCV:     true,
		HasAmount:         true,
		MaxBarsPerRequest: p.maxBars,
		TimestampMode:     marketdata.TimestampModeOpen,
		// The public endpoint returns one bounded latest page. Keep the common
		// history contract conservative until a cursor-paginated feed is added.
		History:   marketdata.KlineHistoryCapability{MaxLookback: 24 * time.Hour},
		RateLimit: p.rateLimit,
	}
}

func (p *Provider) InstrumentSpec() marketdata.InstrumentSpec {
	return marketdata.InstrumentSpec{
		Markets:      []string{"stockcn"},
		Exchanges:    []string{"XSHG", "XSHE", "XBSE"},
		FullSnapshot: true,
		PageSize:     500,
		RateLimit:    p.instrumentRateLimit,
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
	beginDate := "0"
	endDate := "20500101"
	if !req.StartTime.IsZero() {
		beginDate = req.StartTime.UTC().Format("20060102")
	}
	if !req.EndTime.IsZero() {
		endDate = req.EndTime.UTC().Format("20060102")
	} else if !req.Now.IsZero() {
		endDate = req.Now.UTC().Format("20060102")
	}
	requestLimit := req.Limit
	historicalRequest := !req.StartTime.IsZero() || !req.EndTime.IsZero()
	if historicalRequest {
		requestLimit = p.maxBars
	}
	query := "secid=" + url.QueryEscape(secid) +
		"&klt=1&fqt=0&beg=" + url.QueryEscape(beginDate) +
		"&end=" + url.QueryEscape(endDate) + "&lmt=" + strconv.Itoa(requestLimit) +
		"&fields1=f1,f2,f3,f4,f5,f6,f7,f8,f9,f10,f11,f12,f13" +
		"&fields2=f51,f52,f53,f54,f55,f56,f57,f58,f59,f60,f61"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+p.klineEndpoint+"?"+query, nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("User-Agent", "Mozilla/5.0")
	httpReq.Header.Set("Accept", "*/*")
	httpReq.Header.Set("Referer", "https://quote.eastmoney.com/")
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
	var payload eastMoneyResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("%w: %v", marketdata.ErrProtocol, err)
	}
	lines := payload.Data.Klines
	if !historicalRequest && len(lines) > req.Limit {
		lines = lines[len(lines)-req.Limit:]
	}
	rows := make([]marketdata.NormalizedKline, 0, len(lines))
	now := p.now().UTC()
	for _, line := range lines {
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
		row.SourceID = p.sourceID
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
	declaredTotal := 0
	itemsSeen := 0
	sawUnverifiedShortPage := false
	for page := 1; ; page++ {
		if page > maxInstrumentPages {
			return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: eastmoney instrument pagination exceeded %d pages", marketdata.ErrProtocol, maxInstrumentPages)
		}
		query := url.Values{
			"pn":     {strconv.Itoa(page)},
			"pz":     {strconv.Itoa(spec.PageSize)},
			"fs":     {"m:0 t:6,m:0 t:80,m:1 t:2"},
			"fid":    {"f12"},
			"fields": {"f12,f13,f14,f115,f152,f103,f128,f129"},
		}
		var body []byte
		if err := func(pageCtx context.Context) error {
			httpReq, requestErr := http.NewRequestWithContext(pageCtx, http.MethodGet, p.baseURL+"/api/qt/clist/get?"+query.Encode(), nil)
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
			return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: eastmoney instrument payload missing data", marketdata.ErrProtocol)
		}
		if total, ok := commonsrc.IntField(data, "total", "count", "total_count", "totalnum", "total_nums"); ok && total > 0 {
			if declaredTotal == 0 {
				declaredTotal = total
			} else if declaredTotal != total {
				return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: eastmoney instrument total changed from %d to %d on page %d", marketdata.ErrProtocol, declaredTotal, total, page)
			}
		}
		items := commonsrc.ItemSlice(data, "diff", "clist", "list")
		if len(items) == 0 {
			if diff := commonsrc.ObjectAt(data, "diff"); len(diff) > 0 {
				keys := make([]string, 0, len(diff))
				for key := range diff {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				for _, key := range keys {
					if item, ok := diff[key].(map[string]any); ok {
						items = append(items, item)
					}
				}
			}
		}
		totalPages := commonsrc.PageLimit(data, spec.PageSize)
		hasMore, hasMoreKnown := commonsrc.BoolField(data, "hasnext", "has_next", "hasMore", "has_more")
		if declaredTotal == 0 && len(items) > 0 && len(items) < spec.PageSize {
			sawUnverifiedShortPage = true
			if (totalPages > 0 && page < totalPages) || (hasMoreKnown && hasMore) {
				return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: eastmoney instrument page %d is short (%d < %d) before a declared terminal page", marketdata.ErrProtocol, page, len(items), spec.PageSize)
			}
		}
		itemsSeen += len(items)
		if declaredTotal > 0 && itemsSeen > declaredTotal {
			return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: eastmoney instrument items %d exceed declared total %d on page %d", marketdata.ErrProtocol, itemsSeen, declaredTotal, page)
		}
		if len(items) == 0 {
			if declaredTotal > 0 && itemsSeen != declaredTotal {
				return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: eastmoney instrument total %d does not match %d items on terminal page %d", marketdata.ErrProtocol, declaredTotal, itemsSeen, page)
			}
			if declaredTotal == 0 && sawUnverifiedShortPage {
				return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: eastmoney instrument ended after an unverified short page", marketdata.ErrProtocol)
			}
			if page == 1 || (totalPages > 0 && page < totalPages) || (hasMoreKnown && hasMore) {
				return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: eastmoney instrument page %d is empty before terminal evidence", marketdata.ErrProtocol, page)
			}
			// Without an explicit total or has-more marker, the extra empty page is
			// the provider's terminal signal; a short final page is valid. Body
			// truncation is still rejected by io.ReadAll before this point.
			builder.NextPage()
			break
		}
		for _, item := range items {
			instrument, err := commonsrc.InstrumentFromFields(
				commonsrc.StringField(item, "f12", "code", "symbol"),
				commonsrc.StringField(item, "symbol", "provider_symbol"),
				commonsrc.StringField(item, "f13", "market", "exchange"),
				commonsrc.StringField(item, "f14", "name", "f14_name"),
				commonsrc.StringField(item, "f107", "status", "state"),
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
				if declaredTotal > 0 && itemsSeen != declaredTotal {
					return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: eastmoney instrument total %d does not match %d items after page %d", marketdata.ErrProtocol, declaredTotal, itemsSeen, page)
				}
				if declaredTotal == 0 && sawUnverifiedShortPage {
					return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: eastmoney instrument ended after an unverified short page", marketdata.ErrProtocol)
				}
				break
			}
			continue
		}
		if hasMore, ok := commonsrc.BoolField(data, "hasnext", "has_next", "hasMore", "has_more"); ok {
			if !hasMore {
				if declaredTotal > 0 && itemsSeen != declaredTotal {
					return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: eastmoney instrument total %d does not match %d items after page %d", marketdata.ErrProtocol, declaredTotal, itemsSeen, page)
				}
				if declaredTotal == 0 && sawUnverifiedShortPage {
					return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: eastmoney instrument ended after an unverified short page", marketdata.ErrProtocol)
				}
				break
			}
			continue
		}
		// A short page is not terminal evidence unless the response also
		// carries total/page or has-more metadata. Fetch the next page and
		// require an empty terminal response instead.
	}
	return builder.Snapshot()
}

const maxInstrumentPages = 128
