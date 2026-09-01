package sw

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

const defaultDomain = "www.swsresearch.com"

// Client implements the daily/weekly/monthly trend endpoint used by
// AkShare index_hist_sw. The source is catalog-only until live field units and
// index coverage are validated.
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
		ProviderID: "sw", SourceID: "index_sw_http", Status: marketdata.SourceCatalogOnly, ProtocolVariant: marketdata.ProtocolHTTP,
		Transport: "https", Host: c.domain(), Port: 443, Markets: []string{"stock_cn"},
		InstrumentTypes: []string{"index"}, Frequencies: []string{"1d", "1w", "1M"},
	}
}

func (c *Client) KlineSpec() marketdata.KlineSpec {
	return marketdata.KlineSpec{
		MarketID: "stock_cn", InstrumentType: "index", Frequencies: []string{"1d", "1w", "1M"},
		CompleteOHLCV: true, HasAmount: true, VolumeUnit: "share", AmountUnit: "cny",
		TimestampMode: "start-label", SupportsRange: true, MaxBarsPerRequest: 5000,
		RequestTimeoutSeconds: 5, HistoryStart: "2000-01-01",
	}
}

func (c *Client) FetchKlines(ctx context.Context, request marketdata.KlineRequest) ([]marketdata.NormalizedKline, error) {
	if c == nil || c.HTTP == nil {
		return nil, fmt.Errorf("sw: client is not initialized")
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if !c.KlineSpec().SupportsMarketInstrument(request.MarketID, request.InstrumentType) {
		return nil, fmt.Errorf("sw: unsupported market/instrument %s/%s", request.MarketID, request.InstrumentType)
	}
	period, err := periodValue(request.Frequency)
	if err != nil {
		return nil, err
	}
	symbol, err := normalizeSymbol(request.ProviderSymbol)
	if err != nil {
		return nil, err
	}
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return nil, err
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	start := request.StartTime
	if start.IsZero() {
		start = time.Date(2000, time.January, 1, 0, 0, 0, 0, location)
	}
	end := request.EndTime
	if end.IsZero() {
		end = now()
	}
	start = start.In(location)
	end = end.In(location)
	if end.Before(start) {
		return nil, fmt.Errorf("sw: end_time cannot be before start_time")
	}
	query := url.Values{"swindexcode": {symbol}, "period": {period}}
	var payload response
	if err := c.HTTP.Get(ctx, c.domain(), "/institute-sw/api/index_publish/trend/", query, &payload); err != nil {
		return nil, fmt.Errorf("sw: fetch %s: %w", symbol, err)
	}
	if payload.Code != 0 {
		return nil, fmt.Errorf("sw: upstream response code %d: %s", payload.Code, strings.TrimSpace(payload.Msg))
	}
	rows, err := payload.rows()
	if err != nil {
		return nil, fmt.Errorf("sw: decode %s: %w", symbol, err)
	}
	bars, err := parseRows(rows, request, location, now())
	if err != nil {
		return nil, fmt.Errorf("sw: parse %s: %w", symbol, err)
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
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

func (r response) rows() ([]map[string]json.RawMessage, error) {
	if len(r.Data) == 0 || string(r.Data) == "null" {
		return nil, nil
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(r.Data, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func parseRows(rows []map[string]json.RawMessage, request marketdata.KlineRequest, location *time.Location, fetchedAt time.Time) ([]marketdata.NormalizedKline, error) {
	result := make([]marketdata.NormalizedKline, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for index, row := range rows {
		date, err := rowString(row, "bargaindate")
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
		open, err := rowNumber(row, "openindex")
		if err != nil {
			return nil, fmt.Errorf("row %d open: %w", index, err)
		}
		high, err := rowNumber(row, "maxindex")
		if err != nil {
			return nil, fmt.Errorf("row %d high: %w", index, err)
		}
		low, err := rowNumber(row, "minindex")
		if err != nil {
			return nil, fmt.Errorf("row %d low: %w", index, err)
		}
		closeValue, err := rowNumber(row, "closeindex")
		if err != nil {
			return nil, fmt.Errorf("row %d close: %w", index, err)
		}
		volume, err := rowNumber(row, "bargainamount")
		if err != nil {
			return nil, fmt.Errorf("row %d volume: %w", index, err)
		}
		amount, err := rowNumber(row, "bargainsum")
		if err != nil {
			return nil, fmt.Errorf("row %d amount: %w", index, err)
		}
		result = append(result, marketdata.NormalizedKline{
			SubjectID: request.SubjectID, ProviderID: "sw", SourceID: "index_sw_http", ProviderSymbol: request.ProviderSymbol,
			Frequency: request.Frequency, BarStart: barStart.UTC(), BarEnd: addBarPeriod(barStart, request.Frequency).UTC(),
			Open: common.NewDecimal(open), High: common.NewDecimal(high), Low: common.NewDecimal(low), Close: common.NewDecimal(closeValue),
			Volume: common.NewDecimal(volume), Amount: marketdata.OptionalDecimal{Value: common.NewDecimal(amount), Valid: true},
			VolumeUnit: "share", AmountUnit: "cny", ProviderTime: barStart, FetchedAt: fetchedAt.UTC(),
		})
	}
	return result, nil
}

func periodValue(frequency string) (string, error) {
	switch strings.TrimSpace(frequency) {
	case "1d":
		return "DAY", nil
	case "1w":
		return "WEEK", nil
	case "1M":
		return "MONTH", nil
	default:
		return "", fmt.Errorf("sw: unsupported frequency %q", frequency)
	}
}

func rowString(row map[string]json.RawMessage, key string) (string, error) {
	value, ok := row[key]
	if !ok {
		return "", fmt.Errorf("missing %s", key)
	}
	value = []byte(strings.TrimSpace(string(value)))
	if len(value) == 0 || string(value) == "null" {
		return "", fmt.Errorf("%s is empty", key)
	}
	if value[0] == '"' {
		var result string
		if err := json.Unmarshal(value, &result); err != nil {
			return "", err
		}
		result = strings.TrimSpace(result)
		if result == "" {
			return "", fmt.Errorf("%s is empty", key)
		}
		return result, nil
	}
	return string(value), nil
}

func rowNumber(row map[string]json.RawMessage, key string) (string, error) {
	value, err := rowString(row, key)
	if err != nil {
		return "", err
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return "", fmt.Errorf("%s %q is not numeric", key, value)
	}
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return "", fmt.Errorf("%s %q is not finite", key, value)
	}
	return value, nil
}

func parseDate(value string, location *time.Location) (time.Time, error) {
	for _, layout := range []string{"2006-01-02", "20060102", "2006/01/02"} {
		if parsed, err := time.ParseInLocation(layout, strings.TrimSpace(value), location); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported date %q", value)
}

func addBarPeriod(start time.Time, frequency string) time.Time {
	switch frequency {
	case "1w":
		return start.AddDate(0, 0, 7)
	case "1M":
		year, month, day := start.Date()
		nextMonth := time.Date(year, month+1, 1, start.Hour(), start.Minute(), start.Second(), start.Nanosecond(), start.Location())
		lastDay := time.Date(nextMonth.Year(), nextMonth.Month()+1, 0, 0, 0, 0, 0, nextMonth.Location()).Day()
		if day > lastDay {
			day = lastDay
		}
		return time.Date(nextMonth.Year(), nextMonth.Month(), day, start.Hour(), start.Minute(), start.Second(), start.Nanosecond(), start.Location())
	default:
		return start.AddDate(0, 0, 1)
	}
}

func normalizeSymbol(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) != 6 {
		return "", fmt.Errorf("sw: index code %q must contain six digits", value)
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return "", fmt.Errorf("sw: index code %q must contain six digits", value)
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
