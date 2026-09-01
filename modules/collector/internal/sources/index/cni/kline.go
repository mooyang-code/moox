package cni

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/model/common"
	markethttp "github.com/mooyang-code/moox/modules/collector/internal/sources/markethttp/eastmoney"
)

const defaultDomain = "hq.cnindex.com.cn"

// Client implements the daily OHLCV endpoint used by AkShare index_hist_cni.
// The source remains catalog-only until live unit and coverage validation is
// recorded in the market manifest.
type Client struct {
	HTTP   markethttp.Getter
	Domain string
	Now    func() time.Time
}

func NewClient(getter markethttp.Getter) *Client {
	return &Client{HTTP: getter, Domain: defaultDomain}
}

func (c *Client) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{
		ProviderID: "cni", SourceID: "index_cni_http", Status: marketdata.SourceCatalogOnly, ProtocolVariant: marketdata.ProtocolHTTP,
		Transport: "https", Host: c.domain(), Port: 443, Markets: []string{"stock_cn"},
		InstrumentTypes: []string{"index"}, Frequencies: []string{"1d"},
	}
}

func (c *Client) KlineSpec() marketdata.KlineSpec {
	return marketdata.KlineSpec{
		MarketID: "stock_cn", InstrumentType: "index", Frequencies: []string{"1d"},
		// index_hist_cni exposes volume in ten-thousand shares and amount in
		// hundred-million CNY. Keep those source units explicit until a
		// canonical-unit conversion is deliberately enabled.
		CompleteOHLCV: true, HasAmount: true, VolumeUnit: "10k_share", AmountUnit: "100m_cny",
		TimestampMode: "start-label", SupportsRange: true, MaxBarsPerRequest: 5000,
		RequestTimeoutSeconds: 5, HistoryStart: "1990-01-01",
	}
}

func (c *Client) FetchKlines(ctx context.Context, request marketdata.KlineRequest) ([]marketdata.NormalizedKline, error) {
	if c == nil || c.HTTP == nil {
		return nil, fmt.Errorf("cni: client is not initialized")
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if !c.KlineSpec().SupportsMarketInstrument(request.MarketID, request.InstrumentType) {
		return nil, fmt.Errorf("cni: unsupported market/instrument %s/%s", request.MarketID, request.InstrumentType)
	}
	if !c.KlineSpec().SupportsFrequency(request.Frequency) {
		return nil, fmt.Errorf("cni: unsupported frequency %q", request.Frequency)
	}
	symbol, err := normalizeSymbol(request.ProviderSymbol)
	if err != nil {
		return nil, err
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return nil, err
	}
	start := request.StartTime
	if start.IsZero() {
		start = time.Date(1990, time.January, 1, 0, 0, 0, 0, location)
	}
	end := request.EndTime
	if end.IsZero() {
		end = now()
	}
	start = start.In(location)
	end = end.In(location)
	if end.Before(start) {
		return nil, fmt.Errorf("cni: end_time cannot be before start_time")
	}
	query := url.Values{
		"indexCode": {symbol},
		"startDate": {start.Format("2006-01-02")},
		"endDate":   {end.Format("2006-01-02")},
		"frequency": {"day"},
	}
	var payload response
	if err := c.HTTP.Get(ctx, c.domain(), "/market/market/getIndexDailyDataWithDataFormat", query, &payload); err != nil {
		return nil, fmt.Errorf("cni: fetch %s: %w", symbol, err)
	}
	if payload.Code != 0 {
		return nil, fmt.Errorf("cni: upstream response code %d: %s", payload.Code, strings.TrimSpace(payload.Msg))
	}
	if payload.Data == nil {
		return nil, nil
	}
	bars, err := parseRows(payload.Data.Rows, request, location, now())
	if err != nil {
		return nil, fmt.Errorf("cni: parse %s: %w", symbol, err)
	}
	result := make([]marketdata.NormalizedKline, 0, len(bars))
	for _, bar := range bars {
		if bar.BarStart.Before(start.UTC()) || bar.BarStart.After(end.UTC()) {
			continue
		}
		result = append(result, bar)
	}
	sort.SliceStable(result, func(left, right int) bool { return result[left].BarStart.Before(result[right].BarStart) })
	if request.Limit > 0 && len(result) > request.Limit {
		result = result[len(result)-request.Limit:]
	}
	return result, nil
}

type response struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data *struct {
		Rows [][]json.RawMessage `json:"data"`
	} `json:"data"`
}

func parseRows(rows [][]json.RawMessage, request marketdata.KlineRequest, location *time.Location, fetchedAt time.Time) ([]marketdata.NormalizedKline, error) {
	result := make([]marketdata.NormalizedKline, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for index, row := range rows {
		if len(row) < 10 {
			return nil, fmt.Errorf("row %d has %d fields, want at least 10", index, len(row))
		}
		date, err := rawString(row[0])
		if err != nil {
			return nil, fmt.Errorf("row %d date: %w", index, err)
		}
		barStart, err := parseDate(date, location)
		if err != nil {
			return nil, fmt.Errorf("row %d date: %w", index, err)
		}
		key := barStart.UTC().Format(time.RFC3339Nano)
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("duplicate timestamp %s", key)
		}
		seen[key] = struct{}{}
		values := make([]string, 5)
		for valueIndex, fieldIndex := range []int{3, 2, 4, 5, 9} {
			values[valueIndex], err = rawNumber(row[fieldIndex])
			if err != nil {
				return nil, fmt.Errorf("row %d field %d: %w", index, fieldIndex, err)
			}
		}
		amount, err := rawNumber(row[8])
		if err != nil {
			return nil, fmt.Errorf("row %d amount: %w", index, err)
		}
		result = append(result, marketdata.NormalizedKline{
			SubjectID: request.SubjectID, ProviderID: "cni", SourceID: "index_cni_http", ProviderSymbol: request.ProviderSymbol,
			Frequency: request.Frequency, BarStart: barStart.UTC(), BarEnd: barStart.AddDate(0, 0, 1).UTC(),
			Open: common.NewDecimal(values[0]), High: common.NewDecimal(values[1]), Low: common.NewDecimal(values[2]), Close: common.NewDecimal(values[3]),
			Volume: common.NewDecimal(values[4]), Amount: marketdata.OptionalDecimal{Value: common.NewDecimal(amount), Valid: true},
			VolumeUnit: "10k_share", AmountUnit: "100m_cny", ProviderTime: barStart, FetchedAt: fetchedAt.UTC(),
		})
	}
	return result, nil
}

func rawString(value json.RawMessage) (string, error) {
	value = []byte(strings.TrimSpace(string(value)))
	if len(value) == 0 || string(value) == "null" {
		return "", fmt.Errorf("value is empty")
	}
	if value[0] == '"' {
		var decoded string
		if err := json.Unmarshal(value, &decoded); err != nil {
			return "", err
		}
		decoded = strings.TrimSpace(decoded)
		if decoded == "" {
			return "", fmt.Errorf("value is empty")
		}
		return decoded, nil
	}
	return strings.TrimSpace(string(value)), nil
}

func rawNumber(value json.RawMessage) (string, error) {
	result, err := rawString(value)
	if err != nil {
		return "", err
	}
	number, err := strconv.ParseFloat(result, 64)
	if err != nil {
		return "", fmt.Errorf("%q is not numeric", result)
	}
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return "", fmt.Errorf("%q is not finite", result)
	}
	return result, nil
}

func parseDate(value string, location *time.Location) (time.Time, error) {
	for _, layout := range []string{"2006-01-02", "20060102", "2006/01/02"} {
		if parsed, err := time.ParseInLocation(layout, strings.TrimSpace(value), location); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported date %q", value)
}

func normalizeSymbol(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) != 6 {
		return "", fmt.Errorf("cni: index code %q must contain six digits", value)
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return "", fmt.Errorf("cni: index code %q must contain six digits", value)
		}
	}
	return value, nil
}

func (c *Client) domain() string {
	if value := strings.TrimSpace(c.Domain); value != "" {
		return value
	}
	return defaultDomain
}

var _ marketdata.KlineFetcher = (*Client)(nil)
