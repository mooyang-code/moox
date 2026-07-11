package ifeng

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
	"github.com/mooyang-code/moox/modules/collector/internal/providers"
)

type Config struct {
	BaseURL    string
	HTTPClient *http.Client
	Now        func() time.Time
}
type Provider struct {
	baseURL  string
	client   *http.Client
	now      func() time.Time
	location *time.Location
}

func New(cfg Config) *Provider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.finance.ifeng.com"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	location, _ := time.LoadLocation("Asia/Shanghai")
	return &Provider{baseURL: strings.TrimRight(cfg.BaseURL, "/"), client: cfg.HTTPClient, now: cfg.Now, location: location}
}
func (*Provider) ID() marketdata.ProviderID { return "ifeng" }
func (*Provider) Capabilities() []providers.Capability {
	return []providers.Capability{{Feed: providers.FeedKline, ProductType: marketdata.ProductEquity, InstrumentType: marketdata.InstrumentEquity, Frequency: marketdata.FrequencyDay}, {Feed: providers.FeedKline, ProductType: marketdata.ProductETF, InstrumentType: marketdata.InstrumentETF, Frequency: marketdata.FrequencyDay}, {Feed: providers.FeedKline, ProductType: marketdata.ProductIndex, InstrumentType: marketdata.InstrumentIndex, Frequency: marketdata.FrequencyDay}, {Feed: providers.FeedKline, ProductType: marketdata.ProductEquity, InstrumentType: marketdata.InstrumentEquity, Frequency: marketdata.Frequency5Min}}
}
func (p *Provider) FetchKlines(ctx context.Context, gate providers.RequestGate, req providers.FetchKlinesRequest) (providers.FetchKlinesResult, error) {
	result := providers.FetchKlinesResult{Complete: true}
	for index, subject := range req.Subjects {
		permit, err := gate.BeforeRequest(ctx, providers.RequestMeta{ProviderID: p.ID(), RequestIndex: index, EndpointClass: "kline", RequestCost: 1})
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
	path, query, err := requestSpec(subject.ProviderSymbol, req.Frequency)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+path+"?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	rsp, err := p.client.Do(request)
	if err != nil {
		return nil, providers.NewError(providers.ErrorTemporarilyUnavailable, "ifeng request", err)
	}
	defer rsp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(rsp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if rsp.StatusCode == 429 {
		return nil, providers.NewError(providers.ErrorRateLimited, "ifeng rate limit", nil)
	}
	if rsp.StatusCode >= 400 {
		return nil, providers.NewError(providers.ErrorTemporarilyUnavailable, fmt.Sprintf("ifeng status %d", rsp.StatusCode), nil)
	}
	var payload struct {
		Record [][]string `json:"record"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, providers.NewError(providers.ErrorParseFailed, "ifeng payload", err)
	}
	rows := make([]marketdata.ProviderKline, 0, len(payload.Record))
	for _, record := range payload.Record {
		if len(record) < 6 {
			return nil, providers.NewError(providers.ErrorParseFailed, "ifeng record has insufficient columns", nil)
		}
		dataTime, closeTime, tradeDate, err := p.bucketTimes(record[0], req.Frequency)
		if err != nil {
			return nil, err
		}
		if !req.StartTime.IsZero() && dataTime.Before(req.StartTime.UTC()) {
			continue
		}
		if !req.EndTime.IsZero() && dataTime.After(req.EndTime.UTC()) {
			continue
		}
		open, err := marketdata.ParseDecimal(stripComma(record[1]))
		if err != nil {
			return nil, err
		}
		high, err := marketdata.ParseDecimal(stripComma(record[2]))
		if err != nil {
			return nil, err
		}
		closeValue, err := marketdata.ParseDecimal(stripComma(record[3]))
		if err != nil {
			return nil, err
		}
		low, err := marketdata.ParseDecimal(stripComma(record[4]))
		if err != nil {
			return nil, err
		}
		volume, err := marketdata.ParseDecimal(stripComma(record[5]))
		if err != nil {
			return nil, err
		}
		rows = append(rows, marketdata.ProviderKline{SubjectID: subject.SubjectID, ProviderID: p.ID(), ProviderSymbol: subject.ProviderSymbol, Frequency: req.Frequency, DataTime: dataTime, CloseTime: closeTime, TradeDate: tradeDate, FeedScope: string(req.InstrumentType), VolumeUnit: "share", AmountUnit: "CNY", Open: open, High: high, Low: low, Close: closeValue, Volume: &volume, ProviderTimestamp: closeTime, FetchedAt: p.now().UTC(), RequestID: "ifeng:" + subject.ProviderSymbol + ":" + dataTime.Format(time.RFC3339), Closed: !closeTime.After(p.now().UTC())})
	}
	return rows, nil
}
func requestSpec(symbol string, frequency marketdata.Frequency) (string, url.Values, error) {
	switch frequency {
	case marketdata.FrequencyDay:
		return "/akdaily/", url.Values{"code": {symbol}, "type": {"last"}}, nil
	case marketdata.FrequencyWeek:
		return "/akweekly/", url.Values{"code": {symbol}, "type": {"last"}}, nil
	case marketdata.Frequency5Min:
		return "/akmin", url.Values{"scode": {symbol}, "type": {"5"}}, nil
	case marketdata.Frequency15Min:
		return "/akmin", url.Values{"scode": {symbol}, "type": {"15"}}, nil
	case marketdata.Frequency30Min:
		return "/akmin", url.Values{"scode": {symbol}, "type": {"30"}}, nil
	case marketdata.FrequencyHour:
		return "/akmin", url.Values{"scode": {symbol}, "type": {"60"}}, nil
	default:
		return "", nil, providers.NewError(providers.ErrorUnsupported, "ifeng frequency", nil)
	}
}
func (p *Provider) bucketTimes(raw string, frequency marketdata.Frequency) (time.Time, time.Time, string, error) {
	if len(raw) == 10 {
		day, err := time.ParseInLocation("2006-01-02", raw, p.location)
		if err != nil {
			return time.Time{}, time.Time{}, "", err
		}
		close := time.Date(day.Year(), day.Month(), day.Day(), 15, 0, 0, 0, p.location)
		return day.UTC(), close.UTC(), raw, nil
	}
	value, err := time.ParseInLocation("2006-01-02 15:04:05", raw, p.location)
	if err != nil {
		return time.Time{}, time.Time{}, "", err
	}
	return value.UTC(), value.Add(time.Duration(frequency.DurationMinutes()) * time.Minute).UTC(), value.Format("2006-01-02"), nil
}
func stripComma(value string) string { return strings.ReplaceAll(strings.TrimSpace(value), ",", "") }
