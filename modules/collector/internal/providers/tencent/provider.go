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
		cfg.BaseURL = "https://web.ifzq.gtimg.cn"
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
func (*Provider) ID() marketdata.ProviderID { return "tencent" }
func (*Provider) Capabilities() []providers.Capability {
	result := []providers.Capability{}
	for _, instrument := range []marketdata.InstrumentType{marketdata.InstrumentEquity, marketdata.InstrumentETF, marketdata.InstrumentIndex} {
		product := marketdata.ProductType(instrument)
		for _, frequency := range []marketdata.Frequency{marketdata.Frequency5Min, marketdata.Frequency15Min, marketdata.Frequency30Min, marketdata.FrequencyHour, marketdata.FrequencyDay} {
			result = append(result, providers.Capability{Feed: providers.FeedKline, ProductType: product, InstrumentType: instrument, Frequency: frequency})
		}
	}
	return result
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
	path, query, key, err := requestSpec(subject.ProviderSymbol, req)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+path+"?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	rsp, err := p.client.Do(request)
	if err != nil {
		return nil, providers.NewError(providers.ErrorTemporarilyUnavailable, "tencent request", err)
	}
	defer rsp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(rsp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if rsp.StatusCode == http.StatusTooManyRequests {
		return nil, providers.NewError(providers.ErrorRateLimited, "tencent rate limit", nil)
	}
	if rsp.StatusCode >= 400 {
		return nil, providers.NewError(providers.ErrorTemporarilyUnavailable, fmt.Sprintf("tencent status %d", rsp.StatusCode), nil)
	}
	payload, err := decodePayload(body)
	if err != nil {
		return nil, providers.NewError(providers.ErrorParseFailed, "tencent payload", err)
	}
	market, ok := payload.Data[subject.ProviderSymbol]
	if !ok {
		return nil, nil
	}
	records := market[key]
	rows := make([]marketdata.ProviderKline, 0, len(records))
	for _, record := range records {
		row, err := p.parseRecord(subject, req, record)
		if err != nil {
			return nil, err
		}
		if !req.StartTime.IsZero() && row.DataTime.Before(req.StartTime.UTC()) {
			continue
		}
		if !req.EndTime.IsZero() && row.DataTime.After(req.EndTime.UTC()) {
			continue
		}
		rows = append(rows, row)
	}
	return rows, nil
}

type responsePayload struct {
	Data map[string]map[string][][]string `json:"data"`
}

func decodePayload(body []byte) (responsePayload, error) {
	raw := strings.TrimSpace(string(body))
	if index := strings.Index(raw, "="); index >= 0 {
		raw = raw[index+1:]
	}
	raw = strings.TrimSuffix(strings.TrimSpace(raw), ";")
	var payload responsePayload
	err := json.Unmarshal([]byte(raw), &payload)
	return payload, err
}
func requestSpec(symbol string, req providers.FetchKlinesRequest) (string, url.Values, string, error) {
	limit := req.Limit
	if limit <= 0 || limit > 640 {
		limit = 640
	}
	switch req.Frequency {
	case marketdata.FrequencyDay:
		query := url.Values{"_var": {"kline_day"}, "param": {strings.Join([]string{symbol, "day", formatDate(req.StartTime), formatDate(req.EndTime), strconv.Itoa(limit), ""}, ",")}}
		return "/appstock/app/kline/get", query, "day", nil
	case marketdata.Frequency5Min, marketdata.Frequency15Min, marketdata.Frequency30Min, marketdata.FrequencyHour:
		minutes := req.Frequency.DurationMinutes()
		key := "m" + strconv.Itoa(minutes)
		query := url.Values{"_var": {key + "_today"}, "param": {strings.Join([]string{symbol, key, "", strconv.Itoa(limit)}, ",")}}
		return "/appstock/app/kline/mkline", query, key, nil
	default:
		return "", nil, "", providers.NewError(providers.ErrorUnsupported, "tencent frequency", nil)
	}
}
func formatDate(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02")
}
func (p *Provider) parseRecord(subject providers.ProviderSubject, req providers.FetchKlinesRequest, record []string) (marketdata.ProviderKline, error) {
	if len(record) < 6 {
		return marketdata.ProviderKline{}, providers.NewError(providers.ErrorParseFailed, "tencent record has insufficient columns", nil)
	}
	dataTime, closeTime, tradeDate, err := p.bucketTimes(record[0], req.Frequency)
	if err != nil {
		return marketdata.ProviderKline{}, err
	}
	open, err := marketdata.ParseDecimal(strip(record[1]))
	if err != nil {
		return marketdata.ProviderKline{}, err
	}
	closeValue, err := marketdata.ParseDecimal(strip(record[2]))
	if err != nil {
		return marketdata.ProviderKline{}, err
	}
	high, err := marketdata.ParseDecimal(strip(record[3]))
	if err != nil {
		return marketdata.ProviderKline{}, err
	}
	low, err := marketdata.ParseDecimal(strip(record[4]))
	if err != nil {
		return marketdata.ProviderKline{}, err
	}
	volume, err := marketdata.ParseDecimal(strip(record[5]))
	if err != nil {
		return marketdata.ProviderKline{}, err
	}
	var amount *marketdata.Decimal
	if len(record) > 6 && strings.TrimSpace(record[6]) != "" {
		value, err := marketdata.ParseDecimal(strip(record[6]))
		if err != nil {
			return marketdata.ProviderKline{}, err
		}
		amount = &value
	}
	return marketdata.ProviderKline{SubjectID: subject.SubjectID, ProviderID: p.ID(), ProviderSymbol: subject.ProviderSymbol, Frequency: req.Frequency, DataTime: dataTime, CloseTime: closeTime, TradeDate: tradeDate, FeedScope: string(req.InstrumentType), VolumeUnit: "share", AmountUnit: "CNY", Open: open, High: high, Low: low, Close: closeValue, Volume: &volume, Amount: amount, ProviderTimestamp: closeTime, FetchedAt: p.now().UTC(), RequestID: "tencent:" + subject.ProviderSymbol + ":" + dataTime.Format(time.RFC3339), Closed: !closeTime.After(p.now().UTC())}, nil
}
func (p *Provider) bucketTimes(raw string, frequency marketdata.Frequency) (time.Time, time.Time, string, error) {
	if len(raw) == 10 {
		day, err := time.ParseInLocation("2006-01-02", raw, p.location)
		if err != nil {
			return time.Time{}, time.Time{}, "", err
		}
		closeTime := time.Date(day.Year(), day.Month(), day.Day(), 15, 0, 0, 0, p.location)
		return day.UTC(), closeTime.UTC(), raw, nil
	}
	value, err := time.ParseInLocation("200601021504", raw, p.location)
	if err != nil {
		return time.Time{}, time.Time{}, "", err
	}
	return value.UTC(), value.Add(time.Duration(frequency.DurationMinutes()) * time.Minute).UTC(), value.Format("2006-01-02"), nil
}
func strip(value string) string { return strings.ReplaceAll(strings.TrimSpace(value), ",", "") }
