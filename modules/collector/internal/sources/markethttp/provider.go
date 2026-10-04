package markethttp

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
)

type SymbolFunc func(string) (string, error)

type Config struct {
	ProviderID        string
	SourceID          string
	DisplayName       string
	MarketID          string
	InstrumentType    marketdata.InstrumentType
	Exchanges         []string
	BaseURL           string
	Endpoint          string
	Host              string
	HTTPClient        *http.Client
	Location          *time.Location
	SymbolFunc        SymbolFunc
	Frequencies       []string
	MaxBarsPerRequest int
	TimestampMode     marketdata.TimestampMode
	CompleteOHLCV     bool
	HasAmount         bool
	VolumeMultiplier  float64
	AmountMultiplier  float64
	Status            marketdata.SourceStatus
	Now               func() time.Time
}

type Provider struct {
	providerID       string
	sourceID         string
	displayName      string
	marketID         string
	instrumentType   marketdata.InstrumentType
	exchanges        []string
	baseURL          string
	endpoint         string
	host             string
	client           *http.Client
	location         *time.Location
	symbolFunc       SymbolFunc
	frequencies      []string
	maxBars          int
	timestampMode    marketdata.TimestampMode
	completeOHLCV    bool
	hasAmount        bool
	volumeMultiplier float64
	amountMultiplier float64
	status           marketdata.SourceStatus
	now              func() time.Time
}

func New(cfg Config) *Provider {
	if strings.TrimSpace(cfg.ProviderID) == "" {
		cfg.ProviderID = "market_http"
	}
	if strings.TrimSpace(cfg.SourceID) == "" {
		cfg.SourceID = "bars_http"
	}
	if strings.TrimSpace(cfg.DisplayName) == "" {
		cfg.DisplayName = cfg.ProviderID + " " + cfg.SourceID
	}
	if strings.TrimSpace(cfg.Endpoint) == "" {
		cfg.Endpoint = "/api/qt/stock/kline/get"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	if cfg.Location == nil {
		cfg.Location = time.UTC
	}
	if len(cfg.Frequencies) == 0 {
		cfg.Frequencies = []string{"1m", "5m", "15m", "30m", "60m", "1d", "1w", "1M"}
	}
	if cfg.MaxBarsPerRequest <= 0 {
		cfg.MaxBarsPerRequest = 1200
	}
	if cfg.TimestampMode == "" {
		cfg.TimestampMode = marketdata.TimestampModeOpen
	}
	if !cfg.CompleteOHLCV {
		cfg.CompleteOHLCV = true
	}
	if cfg.VolumeMultiplier == 0 {
		cfg.VolumeMultiplier = 1
	}
	if cfg.AmountMultiplier == 0 {
		cfg.AmountMultiplier = 1
	}
	if cfg.Status == "" {
		cfg.Status = marketdata.SourceEnabled
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	frequencies := append([]string(nil), cfg.Frequencies...)
	return &Provider{
		providerID: cfg.ProviderID, sourceID: cfg.SourceID, displayName: cfg.DisplayName,
		marketID: cfg.MarketID, instrumentType: cfg.InstrumentType, baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		endpoint: cfg.Endpoint, host: cfg.Host, client: cfg.HTTPClient, location: cfg.Location,
		symbolFunc: cfg.SymbolFunc, exchanges: append([]string(nil), cfg.Exchanges...), frequencies: frequencies, maxBars: cfg.MaxBarsPerRequest,
		timestampMode: cfg.TimestampMode, completeOHLCV: cfg.CompleteOHLCV, hasAmount: cfg.HasAmount,
		volumeMultiplier: cfg.VolumeMultiplier, amountMultiplier: cfg.AmountMultiplier,
		status: cfg.Status, now: cfg.Now,
	}
}

func (p *Provider) Descriptor() marketdata.ProviderDescriptor {
	hosts := []string{p.host}
	if strings.TrimSpace(p.host) == "" {
		hosts = []string{strings.TrimPrefix(p.baseURL, "https://"), strings.TrimPrefix(p.baseURL, "http://")}
	}
	return marketdata.ProviderDescriptor{
		ID: p.providerID, SourceID: p.sourceID, DisplayName: p.displayName, Hosts: hosts,
		ProtocolVariant: "http", Transport: "https", Port: 443, Status: p.status,
	}
}

func (p *Provider) KlineSpec() marketdata.KlineSpec {
	return marketdata.KlineSpec{
		Markets:           []string{p.marketID},
		Exchanges:         append([]string(nil), p.exchanges...),
		Frequencies:       append([]string(nil), p.frequencies...),
		CompleteOHLCV:     p.completeOHLCV,
		HasAmount:         p.hasAmount,
		MaxBarsPerRequest: p.maxBars,
		TimestampMode:     p.timestampMode,
		History:           marketdata.KlineHistoryCapability{SupportsArbitraryRange: true},
		RateLimit: marketdata.RateLimitPolicy{
			RequestsPerSecond: 1, Burst: 1, MaxConcurrent: 1,
			Cooldown: time.Second, RequestTimeout: 10 * time.Second,
		},
	}
}

func (p *Provider) FetchKlines(ctx context.Context, req marketdata.KlineRequest) ([]marketdata.NormalizedKline, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if !strings.EqualFold(strings.TrimSpace(string(req.MarketID)), strings.TrimSpace(p.marketID)) ||
		(req.InstrumentType != "" && req.InstrumentType != p.instrumentType) {
		return nil, fmt.Errorf("%w: %s does not serve market=%s instrument=%s", marketdata.ErrProviderNotFound, p.sourceID, req.MarketID, req.InstrumentType)
	}
	if p.symbolFunc == nil {
		return nil, fmt.Errorf("%w: %s symbol mapping is not configured", marketdata.ErrUnsupportedSymbol, p.sourceID)
	}
	frequency, err := marketdata.ParseFrequency(req.Frequency)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", marketdata.ErrUnsupportedFrequency, err)
	}
	if !containsFrequency(p.frequencies, string(frequency)) {
		return nil, fmt.Errorf("%w: %s does not support %s", marketdata.ErrUnsupportedFrequency, p.sourceID, frequency)
	}
	symbol, err := p.symbolFunc(req.SubjectID)
	if err != nil {
		return nil, err
	}
	requestLimit := req.Limit
	if requestLimit > p.maxBars {
		requestLimit = p.maxBars
	}
	query := url.Values{
		"secid": {symbol}, "klt": {klineType(frequency)}, "fqt": {"0"},
		"beg": {"0"}, "end": {"20500101"}, "lmt": {strconv.Itoa(requestLimit)},
		"fields1": {"f1,f2,f3,f4,f5,f6"}, "fields2": {"f51,f52,f53,f54,f55,f56,f57"},
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+p.endpoint+"?"+query.Encode(), nil)
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
		return nil, fmt.Errorf("%w: read response: %v", marketdata.ErrProtocol, err)
	}
	var payload struct {
		Data struct {
			Klines []string `json:"klines"`
		} `json:"data"`
	}
	body = trimJSONP(body)
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("%w: %v", marketdata.ErrProtocol, err)
	}
	fetchedAt := p.now().UTC()
	now := req.Now.UTC()
	if req.Now.IsZero() {
		now = fetchedAt
	}
	rows := make([]marketdata.NormalizedKline, 0, len(payload.Data.Klines))
	for index, line := range payload.Data.Klines {
		fields := strings.Split(line, ",")
		if len(fields) < 7 || !p.hasAmount {
			return nil, fmt.Errorf("%w: %s row %d does not provide complete OHLCV amount", marketdata.ErrProtocol, p.sourceID, index)
		}
		values := make([]float64, 6)
		for fieldIndex := range values {
			values[fieldIndex], err = parseNumber(fields[fieldIndex+1])
			if err != nil {
				return nil, fmt.Errorf("%w: %s row %d field %d: %v", marketdata.ErrProtocol, p.sourceID, index, fieldIndex+1, err)
			}
		}
		barStart, providerTime, err := p.parseBarTime(fields[0], frequency)
		if err != nil {
			return nil, fmt.Errorf("%w: %s row %d time: %v", marketdata.ErrProtocol, p.sourceID, index, err)
		}
		barEnd := frequency.BarEnd(barStart)
		if barEnd.After(now) {
			continue
		}
		row := marketdata.NormalizedKline{
			SubjectID: req.SubjectID, ProviderID: p.providerID, SourceID: p.sourceID,
			ProviderSymbol: req.ProviderSymbol, Frequency: string(frequency),
			BarStart: barStart, BarEnd: barEnd,
			Open: values[0], Close: values[1], High: values[2], Low: values[3],
			VolumeShares: values[4] * p.volumeMultiplier, AmountCNY: values[5] * p.amountMultiplier,
			ProviderTimestamp: providerTime, FetchedAt: fetchedAt, RequestID: req.RequestID,
		}
		if err := marketdata.ValidateNormalizedKline(row); err != nil {
			return nil, fmt.Errorf("%w: %s row %d: %v", marketdata.ErrProtocol, p.sourceID, index, err)
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil, marketdata.ErrNoClosedBar
	}
	return rows, nil
}

func (p *Provider) parseBarTime(raw string, frequency marketdata.Frequency) (time.Time, time.Time, error) {
	raw = strings.TrimSpace(raw)
	var parsed time.Time
	var err error
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02 15:04:05", "2006-01-02"} {
		parsed, err = time.ParseInLocation(layout, raw, p.location)
		if err == nil {
			break
		}
	}
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	providerTime := parsed
	if frequency == marketdata.FrequencyDay || frequency == marketdata.FrequencyWeek || frequency == marketdata.FrequencyMonth {
		local := parsed.In(p.location)
		if frequency == marketdata.FrequencyMonth {
			parsed = time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, time.UTC)
		} else {
			parsed = time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
		}
	}
	return parsed.UTC(), providerTime.UTC(), nil
}

func parseNumber(value string) (float64, error) {
	return strconv.ParseFloat(strings.TrimSpace(strings.ReplaceAll(value, ",", "")), 64)
}

func containsFrequency(values []string, want string) bool {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == want || (value != string(marketdata.FrequencyMinute) && value != string(marketdata.FrequencyMonth) && strings.EqualFold(value, want)) {
			return true
		}
	}
	return false
}

func trimJSONP(body []byte) []byte {
	body = []byte(strings.TrimSpace(string(body)))
	if len(body) == 0 || body[0] == '{' || body[0] == '[' {
		return body
	}
	startObject, startArray := strings.IndexByte(string(body), '{'), strings.IndexByte(string(body), '[')
	start := -1
	switch {
	case startObject >= 0 && startArray >= 0:
		start = minIndex(startObject, startArray)
	case startObject >= 0:
		start = startObject
	case startArray >= 0:
		start = startArray
	}
	if start < 0 {
		return body
	}
	endObject, endArray := strings.LastIndexByte(string(body), '}'), strings.LastIndexByte(string(body), ']')
	end := maxIndex(endObject, endArray)
	if end < start {
		return body
	}
	return body[start : end+1]
}

func minIndex(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func maxIndex(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func klineType(frequency marketdata.Frequency) string {
	switch frequency {
	case marketdata.FrequencyMinute:
		return "1"
	case marketdata.Frequency5Min:
		return "5"
	case marketdata.Frequency15Min:
		return "15"
	case marketdata.Frequency30Min:
		return "30"
	case marketdata.FrequencyHour:
		return "60"
	case marketdata.FrequencyDay:
		return "101"
	case marketdata.FrequencyWeek:
		return "102"
	case marketdata.FrequencyMonth:
		return "103"
	default:
		return "0"
	}
}
