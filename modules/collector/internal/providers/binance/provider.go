package binance

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
	"github.com/mooyang-code/moox/modules/collector/internal/providers"
)

type Config struct {
	BaseURL        string
	FuturesBaseURL string
	HTTPClient     *http.Client
	Now            func() time.Time
}
type Provider struct {
	baseURL        string
	futuresBaseURL string
	client         *http.Client
	now            func() time.Time
}

func New(cfg Config) *Provider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.binance.com"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	if cfg.FuturesBaseURL == "" {
		cfg.FuturesBaseURL = "https://fapi.binance.com"
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Provider{baseURL: strings.TrimRight(cfg.BaseURL, "/"), futuresBaseURL: strings.TrimRight(cfg.FuturesBaseURL, "/"), client: cfg.HTTPClient, now: cfg.Now}
}
func (*Provider) ID() marketdata.ProviderID { return "binance" }
func (*Provider) Capabilities() []providers.Capability {
	return []providers.Capability{{Feed: providers.FeedInstrument, ProductType: marketdata.ProductSpot, InstrumentType: marketdata.InstrumentSpot}, {Feed: providers.FeedInstrument, ProductType: marketdata.ProductSwap, InstrumentType: marketdata.InstrumentSwap}, {Feed: providers.FeedKline, ProductType: marketdata.ProductSpot, InstrumentType: marketdata.InstrumentSpot, Frequency: marketdata.FrequencyMinute}, {Feed: providers.FeedKline, ProductType: marketdata.ProductSpot, InstrumentType: marketdata.InstrumentSpot, Frequency: marketdata.FrequencyDay}, {Feed: providers.FeedKline, ProductType: marketdata.ProductSwap, InstrumentType: marketdata.InstrumentSwap, Frequency: marketdata.FrequencyMinute}, {Feed: providers.FeedKline, ProductType: marketdata.ProductSwap, InstrumentType: marketdata.InstrumentSwap, Frequency: marketdata.FrequencyDay}}
}

func (p *Provider) FetchInstruments(ctx context.Context, gate providers.RequestGate, req providers.FetchInstrumentsRequest) (providers.FetchInstrumentsResult, error) {
	types := req.InstrumentTypes
	if len(types) == 0 {
		types = []marketdata.InstrumentType{marketdata.InstrumentSpot}
	}
	typeIndex, start, err := instrumentCursor(req.Cursor, len(types))
	if err != nil {
		return providers.FetchInstrumentsResult{}, err
	}
	if typeIndex >= len(types) {
		return providers.FetchInstrumentsResult{Complete: true}, nil
	}
	swap := types[typeIndex] == marketdata.InstrumentSwap
	endpointClass, baseURL, path, cost := "spot_exchange_info", p.baseURL, "/api/v3/exchangeInfo", 20
	if swap {
		endpointClass, baseURL, path, cost = "futures_exchange_info", p.futuresBaseURL, "/fapi/v1/exchangeInfo", 1
	}
	permit, err := gate.BeforeRequest(ctx, providers.RequestMeta{ProviderID: p.ID(), RequestIndex: typeIndex, EndpointClass: endpointClass, RequestCost: int64(cost)})
	if err != nil {
		return providers.FetchInstrumentsResult{}, err
	}
	if !permit.Allowed {
		return providers.FetchInstrumentsResult{}, providers.NewError(providers.ErrorRateLimited, permit.DenialReason, nil)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, nil)
	if err != nil {
		return providers.FetchInstrumentsResult{}, err
	}
	rsp, err := p.client.Do(request)
	if err != nil {
		return providers.FetchInstrumentsResult{}, providers.NewError(providers.ErrorTemporarilyUnavailable, "binance exchange info", err)
	}
	defer rsp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(rsp.Body, 16<<20))
	if err != nil {
		return providers.FetchInstrumentsResult{}, err
	}
	if rsp.StatusCode == http.StatusTooManyRequests {
		return providers.FetchInstrumentsResult{}, providers.NewError(providers.ErrorRateLimited, "binance rate limit", nil)
	}
	if rsp.StatusCode >= 400 {
		return providers.FetchInstrumentsResult{}, providers.NewError(providers.ErrorTemporarilyUnavailable, fmt.Sprintf("binance status %d", rsp.StatusCode), nil)
	}
	var payload struct {
		Symbols []struct{ Symbol, Status, BaseAsset, QuoteAsset string } `json:"symbols"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return providers.FetchInstrumentsResult{}, providers.NewError(providers.ErrorParseFailed, "binance exchange info", err)
	}
	limit := req.Limit
	if limit <= 0 {
		limit = len(payload.Symbols)
	}
	end := start + limit
	if end > len(payload.Symbols) {
		end = len(payload.Symbols)
	}
	if start > end {
		start = end
	}
	pageComplete := end == len(payload.Symbols)
	result := providers.FetchInstrumentsResult{Complete: pageComplete && typeIndex+1 == len(types), RequestCount: 1}
	for _, item := range payload.Symbols[start:end] {
		product, instrument, suffix := marketdata.ProductSpot, marketdata.InstrumentSpot, ""
		if swap {
			product, instrument, suffix = marketdata.ProductSwap, marketdata.InstrumentSwap, "-SWAP"
		}
		result.Instruments = append(result.Instruments, providers.ProviderInstrument{SubjectID: strings.ToUpper(item.BaseAsset + "-" + item.QuoteAsset + suffix), ProviderID: p.ID(), ProviderSymbol: item.Symbol, ExchangeID: "BINANCE", ProductType: product, InstrumentType: instrument, Name: item.BaseAsset + "/" + item.QuoteAsset + suffix, Currency: item.QuoteAsset, Status: strings.ToLower(item.Status), EffectiveAt: req.SnapshotAt.UTC(), FetchedAt: p.now().UTC(), RequestID: "binance:" + endpointClass + ":" + req.SnapshotAt.UTC().Format(time.RFC3339Nano)})
	}
	if !pageComplete {
		result.NextCursor = formatInstrumentCursor(typeIndex, end, len(types))
	} else if typeIndex+1 < len(types) {
		result.NextCursor = formatInstrumentCursor(typeIndex+1, 0, len(types))
	}
	return result, nil
}
func (p *Provider) FetchKlines(ctx context.Context, gate providers.RequestGate, req providers.FetchKlinesRequest) (providers.FetchKlinesResult, error) {
	if req.ProductType != marketdata.ProductSpot && req.ProductType != marketdata.ProductSwap {
		return providers.FetchKlinesResult{}, providers.NewError(providers.ErrorUnsupported, "unsupported Binance product", nil)
	}
	start := p.now()
	result := providers.FetchKlinesResult{Complete: true}
	for index, subject := range req.Subjects {
		endpointClass := "spot_klines"
		if req.ProductType == marketdata.ProductSwap {
			endpointClass = "futures_klines"
		}
		permit, err := gate.BeforeRequest(ctx, providers.RequestMeta{ProviderID: p.ID(), RequestIndex: index, EndpointClass: endpointClass, RequestCost: 1})
		if err != nil {
			return result, err
		}
		if !permit.Allowed {
			return result, providers.NewError(providers.ErrorRateLimited, permit.DenialReason, nil)
		}
		if !permit.NotBefore.IsZero() && permit.NotBefore.After(p.now()) {
			return result, providers.NewError(providers.ErrorRateLimited, "permit not before execution budget", nil)
		}
		rows, err := p.fetchSubject(ctx, subject, req)
		if err != nil {
			return result, err
		}
		result.Rows = append(result.Rows, rows...)
		result.RequestCount++
	}
	result.Latency = p.now().Sub(start)
	return result, nil
}
func (p *Provider) fetchSubject(ctx context.Context, subject providers.ProviderSubject, req providers.FetchKlinesRequest) ([]marketdata.ProviderKline, error) {
	query := url.Values{"symbol": {subject.ProviderSymbol}, "interval": {string(req.Frequency)}, "startTime": {strconv.FormatInt(req.StartTime.UTC().UnixMilli(), 10)}, "endTime": {strconv.FormatInt(req.EndTime.UTC().UnixMilli(), 10)}}
	if req.Limit > 0 {
		query.Set("limit", strconv.Itoa(req.Limit))
	}
	baseURL, path := p.baseURL, "/api/v3/klines"
	if req.ProductType == marketdata.ProductSwap {
		baseURL, path = p.futuresBaseURL, "/fapi/v1/klines"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path+"?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	rsp, err := p.client.Do(request)
	if err != nil {
		return nil, providers.NewError(providers.ErrorTemporarilyUnavailable, "binance request", err)
	}
	defer rsp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(rsp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if rsp.StatusCode == http.StatusTooManyRequests {
		return nil, providers.NewError(providers.ErrorRateLimited, "binance rate limit", nil)
	}
	if rsp.StatusCode >= 400 {
		return nil, providers.NewError(providers.ErrorTemporarilyUnavailable, fmt.Sprintf("binance status %d", rsp.StatusCode), nil)
	}
	var payload [][]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, providers.NewError(providers.ErrorParseFailed, "binance kline payload", err)
	}
	rows := make([]marketdata.ProviderKline, 0, len(payload))
	for _, item := range payload {
		if len(item) < 8 {
			return nil, providers.NewError(providers.ErrorParseFailed, "binance kline has insufficient columns", nil)
		}
		parseText := func(i int) (string, error) {
			var value string
			if err := json.Unmarshal(item[i], &value); err == nil {
				return value, nil
			}
			var number json.Number
			if err := json.Unmarshal(item[i], &number); err != nil {
				return "", err
			}
			return number.String(), nil
		}
		openText, e := parseText(1)
		if e != nil {
			return nil, e
		}
		highText, e := parseText(2)
		if e != nil {
			return nil, e
		}
		lowText, e := parseText(3)
		if e != nil {
			return nil, e
		}
		closeText, e := parseText(4)
		if e != nil {
			return nil, e
		}
		volumeText, e := parseText(5)
		if e != nil {
			return nil, e
		}
		amountText, e := parseText(7)
		if e != nil {
			return nil, e
		}
		open, e := marketdata.ParseDecimal(openText)
		if e != nil {
			return nil, e
		}
		high, e := marketdata.ParseDecimal(highText)
		if e != nil {
			return nil, e
		}
		low, e := marketdata.ParseDecimal(lowText)
		if e != nil {
			return nil, e
		}
		closeValue, e := marketdata.ParseDecimal(closeText)
		if e != nil {
			return nil, e
		}
		volume, e := marketdata.ParseDecimal(volumeText)
		if e != nil {
			return nil, e
		}
		amount, e := marketdata.ParseDecimal(amountText)
		if e != nil {
			return nil, e
		}
		var startMS, endMS int64
		if err := json.Unmarshal(item[0], &startMS); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(item[6], &endMS); err != nil {
			return nil, err
		}
		dataTime := time.UnixMilli(startMS).UTC()
		closeTime := time.UnixMilli(endMS).UTC().Add(time.Millisecond)
		rows = append(rows, marketdata.ProviderKline{SubjectID: subject.SubjectID, ProviderID: p.ID(), ProviderSymbol: subject.ProviderSymbol, Frequency: req.Frequency, DataTime: dataTime, CloseTime: closeTime, TradeDate: dataTime.Format("2006-01-02"), FeedScope: string(req.ProductType), VolumeUnit: "base", AmountUnit: "quote", Open: open, High: high, Low: low, Close: closeValue, Volume: &volume, Amount: &amount, ProviderTimestamp: closeTime, FetchedAt: p.now().UTC(), RequestID: "binance:" + string(req.ProductType) + ":" + dataTime.Format(time.RFC3339Nano), Closed: !closeTime.After(p.now().UTC())})
	}
	return rows, nil
}
func instrumentCursor(raw string, typeCount int) (int, int, error) {
	if raw == "" {
		return 0, 0, nil
	}
	if typeCount == 1 {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			return 0, 0, providers.NewError(providers.ErrorParseFailed, "invalid instrument cursor", err)
		}
		return 0, value, nil
	}
	parts := strings.Split(raw, ":")
	if len(parts) != 2 {
		return 0, 0, providers.NewError(providers.ErrorParseFailed, "invalid instrument cursor", nil)
	}
	typeIndex, err1 := strconv.Atoi(parts[0])
	offset, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || typeIndex < 0 || offset < 0 {
		return 0, 0, providers.NewError(providers.ErrorParseFailed, "invalid instrument cursor", nil)
	}
	return typeIndex, offset, nil
}
func formatInstrumentCursor(typeIndex, offset, typeCount int) string {
	if typeCount == 1 {
		return strconv.Itoa(offset)
	}
	return fmt.Sprintf("%d:%d", typeIndex, offset)
}
