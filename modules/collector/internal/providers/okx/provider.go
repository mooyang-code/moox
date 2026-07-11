package okx

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
		cfg.BaseURL = "https://www.okx.com"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Provider{baseURL: strings.TrimRight(cfg.BaseURL, "/"), client: cfg.HTTPClient, now: cfg.Now}
}
func (*Provider) ID() marketdata.ProviderID { return "okx" }
func (*Provider) Capabilities() []providers.Capability {
	return []providers.Capability{{Feed: providers.FeedInstrument, ProductType: marketdata.ProductSpot, InstrumentType: marketdata.InstrumentSpot}, {Feed: providers.FeedKline, ProductType: marketdata.ProductSpot, InstrumentType: marketdata.InstrumentSpot, Frequency: marketdata.FrequencyMinute}, {Feed: providers.FeedKline, ProductType: marketdata.ProductSpot, InstrumentType: marketdata.InstrumentSpot, Frequency: marketdata.FrequencyDay}}
}

func (p *Provider) FetchInstruments(ctx context.Context, gate providers.RequestGate, req providers.FetchInstrumentsRequest) (providers.FetchInstrumentsResult, error) {
	permit, err := gate.BeforeRequest(ctx, providers.RequestMeta{ProviderID: p.ID(), RequestIndex: 0, EndpointClass: "public_instruments", RequestCost: 1})
	if err != nil {
		return providers.FetchInstrumentsResult{}, err
	}
	if !permit.Allowed {
		return providers.FetchInstrumentsResult{}, providers.NewError(providers.ErrorRateLimited, permit.DenialReason, nil)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/api/v5/public/instruments?instType=SPOT", nil)
	if err != nil {
		return providers.FetchInstrumentsResult{}, err
	}
	rsp, err := p.client.Do(request)
	if err != nil {
		return providers.FetchInstrumentsResult{}, providers.NewError(providers.ErrorTemporarilyUnavailable, "okx instruments", err)
	}
	defer rsp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(rsp.Body, 16<<20))
	if err != nil {
		return providers.FetchInstrumentsResult{}, err
	}
	if rsp.StatusCode == http.StatusTooManyRequests {
		return providers.FetchInstrumentsResult{}, providers.NewError(providers.ErrorRateLimited, "okx rate limit", nil)
	}
	if rsp.StatusCode >= 400 {
		return providers.FetchInstrumentsResult{}, providers.NewError(providers.ErrorTemporarilyUnavailable, fmt.Sprintf("okx status %d", rsp.StatusCode), nil)
	}
	var payload struct {
		Code, Msg string
		Data      []struct {
			InstID   string `json:"instId"`
			BaseCcy  string `json:"baseCcy"`
			QuoteCcy string `json:"quoteCcy"`
			State    string `json:"state"`
			ListTime string `json:"listTime"`
			ExpTime  string `json:"expTime"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return providers.FetchInstrumentsResult{}, providers.NewError(providers.ErrorParseFailed, "okx instruments", err)
	}
	if payload.Code != "0" {
		return providers.FetchInstrumentsResult{}, providers.NewError(providers.ErrorTemporarilyUnavailable, payload.Msg, nil)
	}
	start := 0
	if req.Cursor != "" {
		start, err = strconv.Atoi(req.Cursor)
		if err != nil || start < 0 {
			return providers.FetchInstrumentsResult{}, providers.NewError(providers.ErrorParseFailed, "invalid instrument cursor", err)
		}
	}
	limit := req.Limit
	if limit <= 0 {
		limit = len(payload.Data)
	}
	end := start + limit
	if end > len(payload.Data) {
		end = len(payload.Data)
	}
	if start > end {
		start = end
	}
	result := providers.FetchInstrumentsResult{Complete: end == len(payload.Data), RequestCount: 1}
	for _, item := range payload.Data[start:end] {
		result.Instruments = append(result.Instruments, providers.ProviderInstrument{SubjectID: strings.ToUpper(item.InstID), ProviderID: p.ID(), ProviderSymbol: item.InstID, ExchangeID: "OKX", ProductType: marketdata.ProductSpot, InstrumentType: marketdata.InstrumentSpot, Name: item.BaseCcy + "/" + item.QuoteCcy, Currency: item.QuoteCcy, ListingDate: millisDate(item.ListTime), DelistingDate: millisDate(item.ExpTime), Status: strings.ToLower(item.State), EffectiveAt: req.SnapshotAt.UTC(), FetchedAt: p.now().UTC(), RequestID: "okx:instruments:" + req.SnapshotAt.UTC().Format(time.RFC3339Nano)})
	}
	if !result.Complete {
		result.NextCursor = strconv.Itoa(end)
	}
	return result, nil
}

func millisDate(value string) string {
	milliseconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || milliseconds <= 0 {
		return ""
	}
	return time.UnixMilli(milliseconds).UTC().Format("2006-01-02")
}
func (p *Provider) FetchKlines(ctx context.Context, gate providers.RequestGate, req providers.FetchKlinesRequest) (providers.FetchKlinesResult, error) {
	if req.ProductType != marketdata.ProductSpot {
		return providers.FetchKlinesResult{}, providers.NewError(providers.ErrorUnsupported, "only spot is enabled", nil)
	}
	result := providers.FetchKlinesResult{Complete: true}
	for index, subject := range req.Subjects {
		permit, err := gate.BeforeRequest(ctx, providers.RequestMeta{ProviderID: p.ID(), RequestIndex: index, EndpointClass: "history_candles", RequestCost: 1})
		if err != nil {
			return result, err
		}
		if !permit.Allowed {
			return result, providers.NewError(providers.ErrorRateLimited, permit.DenialReason, nil)
		}
		rows, err := p.fetch(ctx, subject, req)
		if err != nil {
			return result, err
		}
		result.Rows = append(result.Rows, rows...)
		result.RequestCount++
	}
	return result, nil
}
func (p *Provider) fetch(ctx context.Context, subject providers.ProviderSubject, req providers.FetchKlinesRequest) ([]marketdata.ProviderKline, error) {
	query := url.Values{"instId": {subject.ProviderSymbol}, "bar": {okxBar(req.Frequency)}}
	if !req.StartTime.IsZero() {
		query.Set("before", strconv.FormatInt(req.StartTime.UTC().UnixMilli(), 10))
	}
	if !req.EndTime.IsZero() {
		query.Set("after", strconv.FormatInt(req.EndTime.UTC().UnixMilli(), 10))
	}
	if req.Cursor != "" {
		query.Set("after", req.Cursor)
	}
	if req.Limit > 0 {
		query.Set("limit", strconv.Itoa(req.Limit))
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/api/v5/market/history-candles?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	rsp, err := p.client.Do(request)
	if err != nil {
		return nil, providers.NewError(providers.ErrorTemporarilyUnavailable, "okx request", err)
	}
	defer rsp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(rsp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if rsp.StatusCode == 429 {
		return nil, providers.NewError(providers.ErrorRateLimited, "okx rate limit", nil)
	}
	if rsp.StatusCode >= 400 {
		return nil, providers.NewError(providers.ErrorTemporarilyUnavailable, fmt.Sprintf("okx status %d", rsp.StatusCode), nil)
	}
	var payload struct {
		Code string     `json:"code"`
		Msg  string     `json:"msg"`
		Data [][]string `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, providers.NewError(providers.ErrorParseFailed, "okx payload", err)
	}
	if payload.Code != "0" {
		return nil, providers.NewError(providers.ErrorTemporarilyUnavailable, payload.Msg, nil)
	}
	rows := make([]marketdata.ProviderKline, 0, len(payload.Data))
	for _, item := range payload.Data {
		if len(item) < 9 {
			return nil, providers.NewError(providers.ErrorParseFailed, "okx candle has insufficient columns", nil)
		}
		startMS, err := strconv.ParseInt(item[0], 10, 64)
		if err != nil {
			return nil, err
		}
		open, err := marketdata.ParseDecimal(item[1])
		if err != nil {
			return nil, err
		}
		high, err := marketdata.ParseDecimal(item[2])
		if err != nil {
			return nil, err
		}
		low, err := marketdata.ParseDecimal(item[3])
		if err != nil {
			return nil, err
		}
		closeValue, err := marketdata.ParseDecimal(item[4])
		if err != nil {
			return nil, err
		}
		volume, err := marketdata.ParseDecimal(item[5])
		if err != nil {
			return nil, err
		}
		amount, err := marketdata.ParseDecimal(item[7])
		if err != nil {
			return nil, err
		}
		dataTime := time.UnixMilli(startMS).UTC()
		if !req.StartTime.IsZero() && dataTime.Before(req.StartTime.UTC()) {
			continue
		}
		if !req.EndTime.IsZero() && dataTime.After(req.EndTime.UTC()) {
			continue
		}
		closeTime := dataTime.Add(frequencyDuration(req.Frequency))
		rows = append(rows, marketdata.ProviderKline{SubjectID: subject.SubjectID, ProviderID: p.ID(), ProviderSymbol: subject.ProviderSymbol, Frequency: req.Frequency, DataTime: dataTime, CloseTime: closeTime, TradeDate: dataTime.Format("2006-01-02"), FeedScope: "spot", VolumeUnit: "base", AmountUnit: "quote", Open: open, High: high, Low: low, Close: closeValue, Volume: &volume, Amount: &amount, ProviderTimestamp: closeTime, FetchedAt: p.now().UTC(), RequestID: "okx:" + dataTime.Format(time.RFC3339Nano), Closed: item[8] == "1"})
	}
	return rows, nil
}
func okxBar(f marketdata.Frequency) string {
	if f == marketdata.FrequencyDay {
		return "1Dutc"
	}
	return string(f)
}
func frequencyDuration(f marketdata.Frequency) time.Duration {
	return time.Duration(f.DurationMinutes()) * time.Minute
}
