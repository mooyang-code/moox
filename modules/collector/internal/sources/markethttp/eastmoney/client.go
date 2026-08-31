package eastmoney

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/model/common"
)

type Getter interface {
	Get(context.Context, string, string, url.Values, interface{}) error
}

// RawGetter is implemented by HTTP clients that can expose the bounded
// response stream. JSONP-based providers need the original response text and
// cannot use Getter's JSON decoder directly.
type RawGetter interface {
	GetStream(context.Context, string, string, url.Values, func(io.Reader) error) error
}

type Config struct {
	ProviderID     string
	SourceID       string
	MarketID       string
	InstrumentType string
	Domain         string
	Timezone       string
	SecID          func(string) (string, error)
	Frequencies    []string
	VolumeUnit     string
	AmountUnit     string
	HistoryStart   string
}

type Client struct {
	HTTP Getter
	Config
	Now func() time.Time
}

func NewClient(config Config, getter Getter) *Client {
	if getter == nil {
		getter = httpclient.NewHTTPClient()
	}
	if config.Domain == "" {
		config.Domain = "push2his.eastmoney.com"
	}
	if strings.TrimSpace(config.Timezone) == "" {
		config.Timezone = "Asia/Shanghai"
	}
	if len(config.Frequencies) == 0 {
		config.Frequencies = []string{"1d"}
	}
	if config.VolumeUnit == "" {
		config.VolumeUnit = "share"
	}
	if config.AmountUnit == "" {
		config.AmountUnit = "cny"
	}
	return &Client{HTTP: getter, Config: config}
}

func (c *Client) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{ProviderID: c.ProviderID, SourceID: c.SourceID, ProtocolVariant: marketdata.ProtocolHTTP, Transport: "https", Host: c.Domain, Port: 443, Markets: []string{c.MarketID}, InstrumentTypes: []string{c.InstrumentType}, Frequencies: append([]string(nil), c.Frequencies...)}
}

func (c *Client) KlineSpec() marketdata.KlineSpec {
	return marketdata.KlineSpec{MarketID: c.MarketID, InstrumentType: c.InstrumentType, Frequencies: append([]string(nil), c.Frequencies...), CompleteOHLCV: true, HasAmount: c.AmountUnit != "", VolumeUnit: c.VolumeUnit, AmountUnit: c.AmountUnit, TimestampMode: "start-label", SupportsRange: true, MaxBarsPerRequest: 1000, RequestTimeoutSeconds: 5, HistoryStart: c.HistoryStart}
}

func (c *Client) FetchKlines(ctx context.Context, request marketdata.KlineRequest) ([]marketdata.NormalizedKline, error) {
	if c == nil || c.HTTP == nil {
		return nil, fmt.Errorf("eastmoney: client is not initialized")
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if request.MarketID != c.MarketID || request.InstrumentType != c.InstrumentType {
		return nil, fmt.Errorf("eastmoney: unsupported market/instrument %s/%s", request.MarketID, request.InstrumentType)
	}
	if !c.KlineSpec().SupportsFrequency(request.Frequency) {
		return nil, fmt.Errorf("eastmoney: unsupported frequency %q", request.Frequency)
	}
	if c.SecID == nil {
		return nil, fmt.Errorf("eastmoney: symbol converter is not configured")
	}
	secid, err := c.SecID(request.ProviderSymbol)
	if err != nil {
		return nil, err
	}
	query := url.Values{"secid": {secid}, "klt": {klineType(request.Frequency)}, "fqt": {"0"}, "beg": {dateValue(request.StartTime, "0")}, "end": {dateValue(request.EndTime, "20500101")}, "lmt": {strconv.Itoa(request.Limit)}}
	if request.Limit <= 0 || request.Limit > 1000 {
		query.Set("lmt", "1000")
	}
	var payload Response
	if err := c.HTTP.Get(ctx, c.Domain, "/api/qt/stock/kline/get", query, &payload); err != nil {
		return nil, fmt.Errorf("eastmoney: fetch %s: %w", request.ProviderSymbol, err)
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	return Parse(payload, c.Config, request, now())
}

type Response struct {
	RC   int `json:"rc"`
	Data *struct {
		Klines []string `json:"klines"`
	} `json:"data"`
}

func Parse(payload Response, config Config, request marketdata.KlineRequest, now time.Time) ([]marketdata.NormalizedKline, error) {
	if payload.RC != 0 {
		return nil, fmt.Errorf("eastmoney: upstream response code %d", payload.RC)
	}
	if payload.Data == nil {
		return []marketdata.NormalizedKline{}, nil
	}
	location, err := time.LoadLocation(config.Timezone)
	if err != nil {
		return nil, fmt.Errorf("eastmoney: load timezone %q: %w", config.Timezone, err)
	}
	result := make([]marketdata.NormalizedKline, 0, len(payload.Data.Klines))
	seen := make(map[string]struct{}, len(payload.Data.Klines))
	for index, line := range payload.Data.Klines {
		fields := strings.Split(line, ",")
		if len(fields) < 7 {
			return nil, fmt.Errorf("eastmoney: kline %d has %d fields", index, len(fields))
		}
		start, err := parseTimestamp(fields[0], location)
		if err != nil {
			return nil, fmt.Errorf("eastmoney: kline %d timestamp: %w", index, err)
		}
		key := start.UTC().Format(time.RFC3339Nano)
		if _, ok := seen[key]; ok {
			return nil, fmt.Errorf("eastmoney: duplicate timestamp %s", key)
		}
		seen[key] = struct{}{}
		values := make([]string, 5)
		for valueIndex, fieldIndex := range []int{1, 3, 4, 2, 5} {
			values[valueIndex] = strings.TrimSpace(fields[fieldIndex])
			if values[valueIndex] == "" {
				return nil, fmt.Errorf("eastmoney: kline %d required field %d is empty", index, fieldIndex)
			}
		}
		amount := marketdata.OptionalDecimal{Valid: true, Null: true}
		if value := strings.TrimSpace(fields[6]); value != "" {
			amount = marketdata.OptionalDecimal{Value: newDecimal(value), Valid: true}
		}
		result = append(result, marketdata.NormalizedKline{SubjectID: request.SubjectID, ProviderID: config.ProviderID, SourceID: config.SourceID, ProviderSymbol: request.ProviderSymbol, Frequency: request.Frequency, BarStart: start.UTC(), BarEnd: barEnd(start, request.Frequency, location), Open: newDecimal(values[0]), High: newDecimal(values[1]), Low: newDecimal(values[2]), Close: newDecimal(values[3]), Volume: newDecimal(values[4]), Amount: amount, VolumeUnit: config.VolumeUnit, AmountUnit: config.AmountUnit, ProviderTime: start, FetchedAt: now.UTC()})
	}
	return result, nil
}

func newDecimal(value string) common.Decimal { return common.NewDecimal(value) }

func parseTimestamp(value string, location *time.Location) (time.Time, error) {
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"} {
		if parsed, err := time.ParseInLocation(layout, strings.TrimSpace(value), location); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported timestamp %q", value)
}

func klineType(value string) string {
	switch value {
	case "1d":
		return "101"
	case "1w":
		return "102"
	case "1M":
		return "103"
	case "1m":
		return "1"
	case "5m", "15m", "30m", "60m":
		return strings.TrimSuffix(value, "m")
	default:
		return "0"
	}
}

func dateValue(value time.Time, fallback string) string {
	if value.IsZero() {
		return fallback
	}
	return value.In(time.FixedZone("CST", 8*60*60)).Format("20060102")
}

func barEnd(start time.Time, frequency string, location *time.Location) time.Time {
	if location == nil {
		location = time.UTC
	}
	local := start.In(location)
	switch frequency {
	case "1m":
		return local.Add(time.Minute).UTC()
	case "5m":
		return local.Add(5 * time.Minute).UTC()
	case "15m":
		return local.Add(15 * time.Minute).UTC()
	case "30m":
		return local.Add(30 * time.Minute).UTC()
	case "60m":
		return local.Add(time.Hour).UTC()
	case "1w":
		return local.AddDate(0, 0, 7).UTC()
	case "1M":
		return addCalendarMonth(local).UTC()
	default:
		return local.AddDate(0, 0, 1).UTC()
	}
}

func addCalendarMonth(value time.Time) time.Time {
	year, month, day := value.Date()
	firstOfNext := time.Date(year, month+1, 1, value.Hour(), value.Minute(), value.Second(), value.Nanosecond(), value.Location())
	lastDay := firstOfNext.AddDate(0, 1, -1).Day()
	if day > lastDay {
		day = lastDay
	}
	return time.Date(firstOfNext.Year(), firstOfNext.Month(), day, value.Hour(), value.Minute(), value.Second(), value.Nanosecond(), value.Location())
}
