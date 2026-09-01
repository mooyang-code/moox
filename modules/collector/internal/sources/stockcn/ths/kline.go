package ths

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/model/common"
	markethttp "github.com/mooyang-code/moox/modules/collector/internal/sources/markethttp/eastmoney"
)

const (
	defaultDomain = "d.10jqka.com.cn"
	maxYearRows   = 10000
)

// Client implements the annual JS payload used by QUANTAXIS QAThs. The
// upstream endpoint has no documented JSON schema, so this adapter remains
// catalog-only until live unit and amount semantics are independently checked.
type Client struct {
	HTTP   markethttp.RawGetter
	Domain string
	Now    func() time.Time
}

type YearBar struct {
	Date   time.Time
	Open   common.Decimal
	High   common.Decimal
	Low    common.Decimal
	Close  common.Decimal
	Volume common.Decimal
	Amount common.Decimal
	Factor common.Decimal
}

func NewClient(getter markethttp.RawGetter) *Client {
	return &Client{HTTP: getter, Domain: defaultDomain}
}

func (c *Client) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{
		ProviderID: "ths", SourceID: "daily_http", Status: marketdata.SourceCatalogOnly, ProtocolVariant: marketdata.ProtocolHTTP,
		Transport: "https", Host: c.domain(), Port: 443, Markets: []string{"stock_cn"},
		InstrumentTypes: []string{"equity"}, Frequencies: []string{"1d"},
	}
}

func (c *Client) KlineSpec() marketdata.KlineSpec {
	return marketdata.KlineSpec{
		MarketID: "stock_cn", InstrumentType: "equity", Frequencies: []string{"1d"},
		CompleteOHLCV: true, HasAmount: true, VolumeUnit: "share", AmountUnit: "cny",
		TimestampMode: "start-label", SupportsRange: true, MaxBarsPerRequest: maxYearRows,
		RequestTimeoutSeconds: 5, HistoryStart: "1990-01-01",
	}
}

func (c *Client) FetchKlines(ctx context.Context, request marketdata.KlineRequest) ([]marketdata.NormalizedKline, error) {
	if c == nil || c.HTTP == nil {
		return nil, fmt.Errorf("ths: client is not initialized")
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if request.MarketID != "stock_cn" || request.InstrumentType != "equity" {
		return nil, fmt.Errorf("ths: unsupported market/instrument %s/%s", request.MarketID, request.InstrumentType)
	}
	if request.Frequency != "1d" {
		return nil, fmt.Errorf("ths: unsupported frequency %q", request.Frequency)
	}
	code, err := normalizeCode(request.ProviderSymbol)
	if err != nil {
		return nil, err
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	end := request.EndTime
	if end.IsZero() {
		end = now()
	}
	start := request.StartTime
	if start.IsZero() {
		start = time.Date(1990, time.January, 1, 0, 0, 0, 0, time.UTC)
	}
	if end.Before(start) {
		return nil, fmt.Errorf("ths: end_time cannot be before start_time")
	}
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return nil, err
	}
	start = start.In(location)
	end = end.In(location)
	result := make([]marketdata.NormalizedKline, 0)
	seen := make(map[string]struct{})
	for year := start.Year(); year <= end.Year(); year++ {
		payload, fetchErr := c.fetchYear(ctx, code, year)
		if fetchErr != nil {
			return nil, fmt.Errorf("ths: fetch %s year %d: %w", code, year, fetchErr)
		}
		bars, parseErr := ParseYearPayload(payload, location)
		if parseErr != nil {
			return nil, fmt.Errorf("ths: parse %s year %d: %w", code, year, parseErr)
		}
		for _, bar := range bars {
			if bar.Date.Before(start) || bar.Date.After(end) {
				continue
			}
			key := bar.Date.UTC().Format(time.RFC3339)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, marketdata.NormalizedKline{
				SubjectID: request.SubjectID, ProviderID: "ths", SourceID: "daily_http", ProviderSymbol: request.ProviderSymbol,
				Frequency: request.Frequency, BarStart: bar.Date.UTC(), BarEnd: bar.Date.AddDate(0, 0, 1).UTC(),
				Open: bar.Open, High: bar.High, Low: bar.Low, Close: bar.Close, Volume: bar.Volume,
				Amount: marketdata.OptionalDecimal{Value: bar.Amount, Valid: true}, VolumeUnit: "share", AmountUnit: "cny",
				ProviderTime: bar.Date, FetchedAt: now().UTC(),
			})
		}
	}
	sort.SliceStable(result, func(left, right int) bool { return result[left].BarStart.Before(result[right].BarStart) })
	if request.Limit > 0 && len(result) > request.Limit {
		result = result[len(result)-request.Limit:]
	}
	return result, nil
}

func (c *Client) fetchYear(ctx context.Context, code string, year int) ([]byte, error) {
	var payload []byte
	path := fmt.Sprintf("/v2/line/hs_%s/00/%d.js", code, year)
	err := c.HTTP.GetStream(ctx, c.domain(), path, url.Values{}, func(reader io.Reader) error {
		var err error
		payload, err = io.ReadAll(io.LimitReader(reader, 4<<20))
		return err
	})
	return payload, err
}

// ParseYearPayload decodes the quoted semicolon-separated data string from a
// THS JS response. It deliberately rejects a page that only happens to contain
// HTML or a JavaScript error message.
func ParseYearPayload(raw []byte, location *time.Location) ([]YearBar, error) {
	if location == nil {
		location = time.UTC
	}
	data, err := findDataString(raw)
	if err != nil {
		return nil, err
	}
	rows := strings.Split(data, ";")
	result := make([]YearBar, 0, len(rows))
	for index, row := range rows {
		row = strings.TrimSpace(row)
		if row == "" {
			continue
		}
		fields := strings.Split(row, ",")
		if len(fields) < 8 {
			return nil, fmt.Errorf("row %d has %d fields, want at least 8", index, len(fields))
		}
		date, err := parseDate(fields[0], location)
		if err != nil {
			return nil, fmt.Errorf("row %d date: %w", index, err)
		}
		values := make([]common.Decimal, 7)
		for valueIndex, field := range fields[1:8] {
			field = strings.TrimSpace(field)
			if field == "" {
				return nil, fmt.Errorf("row %d field %d is empty", index, valueIndex+1)
			}
			if _, err := strconv.ParseFloat(field, 64); err != nil {
				return nil, fmt.Errorf("row %d field %d is not numeric: %w", index, valueIndex+1, err)
			}
			values[valueIndex] = common.NewDecimal(field)
		}
		result = append(result, YearBar{Date: date, Open: values[0], High: values[1], Low: values[2], Close: values[3], Volume: values[4], Amount: values[5], Factor: values[6]})
	}
	return result, nil
}

func findDataString(raw []byte) (string, error) {
	for offset := 0; offset < len(raw); {
		start := bytes.IndexByte(raw[offset:], '"')
		if start < 0 {
			break
		}
		start += offset
		end := start + 1
		for end < len(raw) {
			if raw[end] == '\\' {
				end += 2
				continue
			}
			if raw[end] == '"' {
				break
			}
			end++
		}
		if end >= len(raw) {
			break
		}
		candidate := string(raw[start+1 : end])
		if strings.Count(candidate, ",") >= 7 && strings.Contains(candidate, ";") {
			return candidate, nil
		}
		offset = end + 1
	}
	return "", fmt.Errorf("THS JS payload has no quoted kline data")
}

func parseDate(value string, location *time.Location) (time.Time, error) {
	for _, layout := range []string{"20060102", "2006-01-02"} {
		if parsed, err := time.ParseInLocation(layout, strings.TrimSpace(value), location); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported date %q", value)
}

func normalizeCode(value string) (string, error) {
	parts := strings.SplitN(strings.ToUpper(strings.TrimSpace(value)), ".", 2)
	if len(parts) == 2 {
		value = parts[1]
	}
	value = strings.TrimSpace(value)
	if len(value) != 6 {
		return "", fmt.Errorf("ths: stock code %q must contain six digits", value)
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return "", fmt.Errorf("ths: stock code %q must contain six digits", value)
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
