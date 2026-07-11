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
		cfg.BaseURL = "https://api.binance.com"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Provider{baseURL: strings.TrimRight(cfg.BaseURL, "/"), client: cfg.HTTPClient, now: cfg.Now}
}
func (*Provider) ID() marketdata.ProviderID { return "binance" }
func (*Provider) Capabilities() []providers.Capability {
	return []providers.Capability{{Feed: providers.FeedKline, ProductType: marketdata.ProductSpot, InstrumentType: marketdata.InstrumentSpot, Frequency: marketdata.FrequencyMinute}, {Feed: providers.FeedKline, ProductType: marketdata.ProductSpot, InstrumentType: marketdata.InstrumentSpot, Frequency: marketdata.FrequencyDay}}
}
func (p *Provider) FetchKlines(ctx context.Context, gate providers.RequestGate, req providers.FetchKlinesRequest) (providers.FetchKlinesResult, error) {
	if req.ProductType != marketdata.ProductSpot {
		return providers.FetchKlinesResult{}, providers.NewError(providers.ErrorUnsupported, "only spot is enabled", nil)
	}
	start := p.now()
	result := providers.FetchKlinesResult{Complete: true}
	for index, subject := range req.Subjects {
		permit, err := gate.BeforeRequest(ctx, providers.RequestMeta{ProviderID: p.ID(), RequestIndex: index, EndpointClass: "spot_klines", RequestCost: 1})
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
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/api/v3/klines?"+query.Encode(), nil)
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
		rows = append(rows, marketdata.ProviderKline{SubjectID: subject.SubjectID, ProviderID: p.ID(), ProviderSymbol: subject.ProviderSymbol, Frequency: req.Frequency, DataTime: dataTime, CloseTime: closeTime, TradeDate: dataTime.Format("2006-01-02"), FeedScope: "spot", VolumeUnit: "base", AmountUnit: "quote", Open: open, High: high, Low: low, Close: closeValue, Volume: &volume, Amount: &amount, ProviderTimestamp: closeTime, FetchedAt: p.now().UTC(), RequestID: "binance:" + dataTime.Format(time.RFC3339Nano), Closed: !closeTime.After(p.now().UTC())})
	}
	return rows, nil
}
