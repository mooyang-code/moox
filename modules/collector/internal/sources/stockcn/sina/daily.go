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
	dailySourceCN = "stock_cn_http"
	dailySourceHK = "stock_hk_http"
	dailySourceUS = "stock_us_http"
)

// DailyClient implements Sina's K2 daily endpoint for one market. The endpoint
// is shared by three markets, but each market has different symbols, currency
// and session rules, so a client is deliberately bound to one market.
type DailyClient struct {
	HTTP     markethttp.RawGetter
	MarketID string
	Now      func() time.Time
}

func NewDailyClient(getter markethttp.RawGetter, marketID string) *DailyClient {
	return &DailyClient{HTTP: getter, MarketID: strings.ToLower(strings.TrimSpace(marketID))}
}

func (c *DailyClient) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{
		ProviderID: "sina", SourceID: c.sourceID(), Status: marketdata.SourceCatalogOnly,
		ProtocolVariant: marketdata.ProtocolHTTP, Transport: "https", Host: "finance.sina.com.cn", Port: 443,
		Markets: []string{c.MarketID}, InstrumentTypes: []string{"equity"}, Frequencies: []string{"1d"},
	}
}

func (c *DailyClient) KlineSpec() marketdata.KlineSpec {
	amountUnit := "cny"
	historyStart := "1990-01-01"
	switch c.MarketID {
	case "stock_hk":
		amountUnit = "hkd"
		historyStart = "2000-01-01"
	case "stock_us":
		// AkShare deliberately drops Sina's US amount field. Keep it out of
		// the canonical contract until a reliable unit is established.
		amountUnit = ""
		historyStart = "1980-01-01"
	}
	return marketdata.KlineSpec{
		MarketID: c.MarketID, InstrumentType: "equity", Frequencies: []string{"1d"},
		CompleteOHLCV: true, HasAmount: amountUnit != "", VolumeUnit: "share", AmountUnit: amountUnit,
		TimestampMode: "start-label", SupportsRange: true, MaxBarsPerRequest: 20000,
		RequestTimeoutSeconds: 10, HistoryStart: historyStart,
	}
}

func (c *DailyClient) FetchKlines(ctx context.Context, request marketdata.KlineRequest) ([]marketdata.NormalizedKline, error) {
	if c == nil || c.HTTP == nil {
		return nil, fmt.Errorf("sina daily: client is not initialized")
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if request.MarketID != c.MarketID || request.InstrumentType != "equity" {
		return nil, fmt.Errorf("sina daily: unsupported market/instrument %s/%s", request.MarketID, request.InstrumentType)
	}
	if request.Frequency != "1d" {
		return nil, fmt.Errorf("sina daily: unsupported frequency %q", request.Frequency)
	}
	symbol, err := c.symbol(request.ProviderSymbol)
	if err != nil {
		return nil, err
	}
	location, err := c.location()
	if err != nil {
		return nil, err
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	start := request.StartTime
	if start.IsZero() {
		start = time.Date(1900, time.January, 1, 0, 0, 0, 0, location)
	}
	end := request.EndTime
	if end.IsZero() {
		end = now()
	}
	start = start.In(location)
	end = end.In(location)
	if end.Before(start) {
		return nil, fmt.Errorf("sina daily: end_time cannot be before start_time")
	}

	var raw []byte
	path := c.path(symbol)
	if err := c.HTTP.GetStream(ctx, "finance.sina.com.cn", path, url.Values{}, func(reader io.Reader) error {
		var err error
		raw, err = io.ReadAll(io.LimitReader(reader, 8<<20))
		return err
	}); err != nil {
		return nil, fmt.Errorf("sina daily: fetch %s: %w", request.ProviderSymbol, err)
	}
	rows, err := ParseDailyPayload(raw)
	if err != nil {
		return nil, fmt.Errorf("sina daily: parse %s: %w", request.ProviderSymbol, err)
	}
	fetchedAt := now().UTC()
	definition := marketdata.BarDefinition{Frequency: "1d", Location: location, TimestampMode: marketdata.TimestampStartLabel}
	result := make([]marketdata.NormalizedKline, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for index, row := range rows {
		label, parseErr := time.ParseInLocation("2006-01-02", row.Date, location)
		if parseErr != nil {
			return nil, fmt.Errorf("sina daily: row %d date: %w", index, parseErr)
		}
		barStart, barEnd, normalizeErr := definition.NormalizeLabel(label)
		if normalizeErr != nil {
			return nil, fmt.Errorf("sina daily: row %d timestamp: %w", index, normalizeErr)
		}
		if barStart.Before(start) || barStart.After(end) {
			continue
		}
		key := barStart.UTC().Format(time.RFC3339Nano)
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("sina daily: duplicate timestamp %s", key)
		}
		seen[key] = struct{}{}
		bar := marketdata.NormalizedKline{
			SubjectID: request.SubjectID, ProviderID: "sina", SourceID: c.sourceID(), ProviderSymbol: request.ProviderSymbol,
			Frequency: request.Frequency, BarStart: barStart, BarEnd: barEnd,
			Open: newDecimal(row.Open), High: newDecimal(row.High), Low: newDecimal(row.Low), Close: newDecimal(row.Close),
			Volume: newDecimal(row.Volume), VolumeUnit: "share", ProviderTime: label, FetchedAt: fetchedAt,
		}
		if c.KlineSpec().HasAmount {
			bar.Amount = marketdata.OptionalDecimal{Value: newDecimal(row.Amount), Valid: true}
			bar.AmountUnit = c.KlineSpec().AmountUnit
		}
		result = append(result, bar)
	}
	sort.SliceStable(result, func(left, right int) bool { return result[left].BarStart.Before(result[right].BarStart) })
	if request.Limit > 0 && len(result) > request.Limit {
		result = result[len(result)-request.Limit:]
	}
	return result, nil
}

func (c *DailyClient) sourceID() string {
	switch c.MarketID {
	case "stock_hk":
		return dailySourceHK
	case "stock_us":
		return dailySourceUS
	default:
		return dailySourceCN
	}
}

func (c *DailyClient) path(symbol string) string {
	switch c.MarketID {
	case "stock_hk":
		return fmt.Sprintf("/stock/hkstock/%s/klc2_kl.js", symbol)
	case "stock_us":
		return fmt.Sprintf("/staticdata/us/%s", symbol)
	default:
		return fmt.Sprintf("/realstock/company/%s/hisdata_klc2/klc_kl.js", symbol)
	}
}

func (c *DailyClient) symbol(value string) (string, error) {
	parts := strings.SplitN(strings.ToUpper(strings.TrimSpace(value)), ".", 2)
	if len(parts) != 2 || parts[1] == "" {
		return "", fmt.Errorf("sina daily: symbol %q must use EXCHANGE.CODE", value)
	}
	code := parts[1]
	switch c.MarketID {
	case "stock_cn":
		return normalizeSymbol(value)
	case "stock_hk":
		if parts[0] != "HK" || len(code) != 5 || !allDigits(code) {
			return "", fmt.Errorf("sina daily hk: symbol %q must use HK.00000", value)
		}
		return code, nil
	case "stock_us":
		if parts[0] != "US" || strings.ContainsAny(code, `/\\"'`) || strings.ContainsAny(code, " \t\r\n") {
			return "", fmt.Errorf("sina daily us: symbol %q is invalid", value)
		}
		return code, nil
	default:
		return "", fmt.Errorf("sina daily: unsupported market %q", c.MarketID)
	}
}

func (c *DailyClient) location() (*time.Location, error) {
	name := "Asia/Shanghai"
	if c.MarketID == "stock_hk" {
		name = "Asia/Hong_Kong"
	}
	if c.MarketID == "stock_us" {
		name = "America/New_York"
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("sina daily: load timezone %q: %w", name, err)
	}
	return location, nil
}

func allDigits(value string) bool {
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

var _ marketdata.KlineFetcher = (*DailyClient)(nil)
