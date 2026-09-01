package sina

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	markethttp "github.com/mooyang-code/moox/modules/collector/internal/sources/markethttp/eastmoney"
)

const (
	defaultDomain = "quotes.sina.cn"
	minutePath    = "/cn/api/jsonp_v2.php/=/CN_MarketDataService.getKLineData"
	maxRows       = 1970
)

// Client implements Sina's unadjusted A-share minute JSONP endpoint. Sina's
// daily endpoint is a separate compressed JavaScript protocol and is not
// silently treated as a minute response by this adapter.
type Client struct {
	HTTP   markethttp.RawGetter
	Domain string
	Now    func() time.Time
}

func NewClient(getter markethttp.RawGetter) *Client {
	return &Client{HTTP: getter, Domain: defaultDomain}
}

func (c *Client) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{
		ProviderID: "sina", SourceID: "stock_cn_minute_http", Status: marketdata.SourceCatalogOnly,
		ProtocolVariant: marketdata.ProtocolHTTP, Transport: "https", Host: c.domain(), Port: 443,
		Markets: []string{"stock_cn"}, InstrumentTypes: []string{"equity"},
		Frequencies: []string{"1m", "5m", "15m", "30m", "60m"},
	}
}

func (c *Client) KlineSpec() marketdata.KlineSpec {
	return marketdata.KlineSpec{
		MarketID: "stock_cn", InstrumentType: "equity",
		Frequencies:   []string{"1m", "5m", "15m", "30m", "60m"},
		CompleteOHLCV: true, HasAmount: true, VolumeUnit: "share", AmountUnit: "cny",
		TimestampMode: "end-label", SupportsRange: false, MaxBarsPerRequest: maxRows,
		RequestTimeoutSeconds: 5,
	}
}

func (c *Client) FetchKlines(ctx context.Context, request marketdata.KlineRequest) ([]marketdata.NormalizedKline, error) {
	if c == nil || c.HTTP == nil {
		return nil, fmt.Errorf("sina: client is not initialized")
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if !c.KlineSpec().SupportsMarketInstrument(request.MarketID, request.InstrumentType) {
		return nil, fmt.Errorf("sina: unsupported market/instrument %s/%s", request.MarketID, request.InstrumentType)
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
		return nil, fmt.Errorf("sina: load timezone: %w", err)
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
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
		return nil, fmt.Errorf("sina: end_time cannot be before start_time")
	}

	query := url.Values{
		"symbol":  {symbol},
		"scale":   {period},
		"ma":      {"no"},
		"datalen": {fmt.Sprint(maxRows)},
	}
	var raw []byte
	if err := c.HTTP.GetStream(ctx, c.domain(), minutePath, query, func(reader io.Reader) error {
		var readErr error
		raw, readErr = io.ReadAll(io.LimitReader(reader, 4<<20))
		return readErr
	}); err != nil {
		return nil, fmt.Errorf("sina: fetch %s: %w", request.ProviderSymbol, err)
	}
	rows, err := ParseMinutePayload(raw)
	if err != nil {
		return nil, fmt.Errorf("sina: parse %s: %w", request.ProviderSymbol, err)
	}
	fetchedAt := now().UTC()
	barDefinition := marketdata.BarDefinition{
		Frequency: request.Frequency, Location: location, TimestampMode: marketdata.TimestampEndLabel,
	}
	result := make([]marketdata.NormalizedKline, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for index, row := range rows {
		label, parseErr := parseTimestamp(row.Day, location)
		if parseErr != nil {
			return nil, fmt.Errorf("sina: row %d timestamp: %w", index, parseErr)
		}
		barStart, barEnd, normalizeErr := barDefinition.NormalizeLabel(label)
		if normalizeErr != nil {
			return nil, fmt.Errorf("sina: row %d timestamp: %w", index, normalizeErr)
		}
		if barStart.Before(start) || barStart.After(end) {
			continue
		}
		key := barStart.UTC().Format(time.RFC3339Nano)
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("sina: duplicate timestamp %s", key)
		}
		seen[key] = struct{}{}
		result = append(result, marketdata.NormalizedKline{
			SubjectID: request.SubjectID, ProviderID: "sina", SourceID: "stock_cn_minute_http",
			ProviderSymbol: request.ProviderSymbol, Frequency: request.Frequency,
			BarStart: barStart, BarEnd: barEnd,
			Open: newDecimal(row.Open), High: newDecimal(row.High), Low: newDecimal(row.Low), Close: newDecimal(row.Close),
			Volume: newDecimal(row.Volume), Amount: marketdata.OptionalDecimal{Value: newDecimal(row.Amount), Valid: true},
			VolumeUnit: "share", AmountUnit: "cny", ProviderTime: label, FetchedAt: fetchedAt,
		})
	}
	sort.SliceStable(result, func(left, right int) bool { return result[left].BarStart.Before(result[right].BarStart) })
	if request.Limit > 0 && len(result) > request.Limit {
		result = result[len(result)-request.Limit:]
	}
	return result, nil
}

func (c *Client) domain() string {
	if value := strings.TrimSpace(c.Domain); value != "" {
		return value
	}
	return defaultDomain
}

func periodValue(frequency string) (string, error) {
	switch strings.TrimSpace(frequency) {
	case "1m":
		return "1", nil
	case "5m":
		return "5", nil
	case "15m":
		return "15", nil
	case "30m":
		return "30", nil
	case "60m":
		return "60", nil
	default:
		return "", fmt.Errorf("sina: unsupported frequency %q", frequency)
	}
}

var _ marketdata.KlineFetcher = (*Client)(nil)
