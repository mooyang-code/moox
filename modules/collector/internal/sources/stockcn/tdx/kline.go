package tdx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/model/common"
	tdxwire "github.com/mooyang-code/moox/packages/tdx"
)

type Client struct {
	Wire            *tdxwire.NormalClient
	MarketID        string
	MarketIDs       []string
	InstrumentType  string
	InstrumentTypes []string
	Index           bool
	Now             func() time.Time
}

func NewClient(wire *tdxwire.NormalClient, marketID, instrumentType string, index bool) *Client {
	return &Client{Wire: wire, MarketID: marketID, InstrumentType: instrumentType, Index: index}
}

func NewMultiClient(wire *tdxwire.NormalClient, marketIDs, instrumentTypes []string) *Client {
	return &Client{Wire: wire, MarketIDs: append([]string(nil), marketIDs...), InstrumentTypes: append([]string(nil), instrumentTypes...)}
}

func (c *Client) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{ProviderID: "tdx", SourceID: "normal_7709", ProtocolVariant: marketdata.ProtocolTDXNormal, Transport: "tcp", Port: 7709, Markets: c.markets(), InstrumentTypes: c.instrumentTypes(), Frequencies: []string{"1m", "5m", "15m", "30m", "60m", "1d", "1w", "1M"}}
}

func (c *Client) KlineSpec() marketdata.KlineSpec {
	spec := marketdata.KlineSpec{MarketID: c.MarketID, InstrumentType: c.InstrumentType, MarketIDs: append([]string(nil), c.MarketIDs...), InstrumentTypes: append([]string(nil), c.InstrumentTypes...), Frequencies: c.Descriptor().Frequencies, CompleteOHLCV: true, HasAmount: true, VolumeUnit: "share", AmountUnit: "cny", TimestampMode: "start-label", SupportsRange: false, MaxBarsPerRequest: 800, RequestTimeoutSeconds: 15, HistoryStart: "1990-12-19"}
	return spec
}

func (c *Client) InstrumentSpec() marketdata.InstrumentSpec {
	return marketdata.InstrumentSpec{MarketID: c.MarketID, InstrumentType: c.InstrumentType, MarketIDs: append([]string(nil), c.MarketIDs...), InstrumentTypes: append([]string(nil), c.InstrumentTypes...), SupportsFull: true, SupportsPaging: true, HasStatus: false}
}

func (c *Client) FetchInstruments(ctx context.Context, request marketdata.InstrumentRequest) (marketdata.InstrumentSnapshot, error) {
	if c == nil || c.Wire == nil {
		return marketdata.InstrumentSnapshot{}, fmt.Errorf("tdx: normal client is not initialized")
	}
	if !c.supports(request.MarketID, request.InstrumentType) {
		return marketdata.InstrumentSnapshot{}, fmt.Errorf("tdx: unsupported market/instrument %s/%s", request.MarketID, request.InstrumentType)
	}
	if err := request.Validate(); err != nil {
		return marketdata.InstrumentSnapshot{}, err
	}
	var market tdxwire.Market
	var err error
	if request.PageSize <= 0 {
		request.PageSize = 1000
	}
	market, _, err = parseSymbol(request.ProviderSymbol)
	if err != nil {
		// Instrument requests may omit a symbol; in that case use the whole
		// market. A supplied symbol is still validated by the same converter.
		if request.ProviderSymbol != "" {
			return marketdata.InstrumentSnapshot{}, err
		}
		switch strings.ToUpper(strings.TrimSpace(request.ExchangeID)) {
		case "SH":
			market = tdxwire.MarketSH
		case "BJ":
			market = tdxwire.MarketBJ
		default:
			market = tdxwire.MarketSZ
		}
	}
	start := request.Page * request.PageSize
	items, err := c.Wire.SecurityList(ctx, market, start)
	if err != nil {
		return marketdata.InstrumentSnapshot{}, err
	}
	result := marketdata.InstrumentSnapshot{MarketID: request.MarketID, InstrumentType: request.InstrumentType, Items: make([]marketdata.Instrument, 0, len(items))}
	digest := sha256.New()
	for _, item := range items {
		exchange := "SZ"
		if item.Market == tdxwire.MarketSH {
			exchange = "SH"
		} else if item.Market == tdxwire.MarketBJ {
			exchange = "BJ"
		}
		symbol := exchange + "." + item.Code
		result.Items = append(result.Items, marketdata.Instrument{SubjectID: symbol, ProviderSymbol: symbol, Name: item.Name, ExchangeID: exchange, InstrumentType: request.InstrumentType})
		_, _ = digest.Write([]byte(symbol + "\x00" + item.Name + "\n"))
	}
	result.Version = hex.EncodeToString(digest.Sum(nil))
	return result, nil
}

func (c *Client) FetchKlines(ctx context.Context, request marketdata.KlineRequest) ([]marketdata.NormalizedKline, error) {
	if c == nil || c.Wire == nil {
		return nil, fmt.Errorf("tdx: normal client is not initialized")
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if !c.supports(request.MarketID, request.InstrumentType) {
		return nil, fmt.Errorf("tdx: unsupported market/instrument %s/%s", request.MarketID, request.InstrumentType)
	}
	market, code, err := parseSymbol(request.ProviderSymbol)
	if err != nil {
		return nil, err
	}
	category, err := category(request.Frequency)
	if err != nil {
		return nil, err
	}
	limit := request.Limit
	if limit <= 0 || limit > 800 {
		limit = 800
	}
	bars, err := c.Wire.SecurityBars(ctx, market, code, category, 0, limit, c.Index || request.InstrumentType == "index")
	if err != nil {
		return nil, fmt.Errorf("tdx: fetch %s: %w", request.ProviderSymbol, err)
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	result := make([]marketdata.NormalizedKline, 0, len(bars))
	for _, bar := range bars {
		start := bar.Time
		if !category.Intraday() {
			start = time.Date(bar.Time.Year(), bar.Time.Month(), bar.Time.Day(), 0, 0, 0, 0, bar.Time.Location())
		}
		result = append(result, marketdata.NormalizedKline{SubjectID: request.SubjectID, ProviderID: "tdx", SourceID: "normal_7709", ProviderSymbol: request.ProviderSymbol, Frequency: request.Frequency, BarStart: start.UTC(), BarEnd: barEnd(start, request.Frequency), Open: decimal(bar.Open), High: decimal(bar.High), Low: decimal(bar.Low), Close: decimal(bar.Close), Volume: decimal(bar.Volume), Amount: marketdata.OptionalDecimal{Value: decimal(bar.Amount), Valid: true}, VolumeUnit: "share", AmountUnit: "cny", ProviderTime: bar.Time, FetchedAt: now().UTC()})
	}
	return result, nil
}

func (c *Client) supports(marketID, instrumentType string) bool {
	if c == nil {
		return false
	}
	return c.Descriptor().SupportsMarketInstrument(marketID, instrumentType)
}

func (c *Client) markets() []string {
	if len(c.MarketIDs) > 0 {
		return append([]string(nil), c.MarketIDs...)
	}
	return []string{c.MarketID}
}

func (c *Client) instrumentTypes() []string {
	if len(c.InstrumentTypes) > 0 {
		return append([]string(nil), c.InstrumentTypes...)
	}
	return []string{c.InstrumentType}
}

func parseSymbol(value string) (tdxwire.Market, string, error) {
	parts := strings.SplitN(strings.ToUpper(strings.TrimSpace(value)), ".", 2)
	if len(parts) != 2 || parts[1] == "" {
		return 0, "", fmt.Errorf("tdx: symbol %q must use EXCHANGE.CODE", value)
	}
	switch parts[0] {
	case "SH":
		return tdxwire.MarketSH, parts[1], nil
	case "SZ":
		return tdxwire.MarketSZ, parts[1], nil
	case "BJ":
		return tdxwire.MarketBJ, parts[1], nil
	default:
		return 0, "", fmt.Errorf("tdx: unsupported exchange %q", parts[0])
	}
}

func category(value string) (tdxwire.KlineCategory, error) {
	switch strings.TrimSpace(value) {
	case "1m":
		return tdxwire.Category1Min, nil
	case "5m":
		return tdxwire.Category5Min, nil
	case "15m":
		return tdxwire.Category15Min, nil
	case "30m":
		return tdxwire.Category30Min, nil
	case "60m":
		return tdxwire.Category60Min, nil
	case "1d":
		return tdxwire.CategoryDay, nil
	case "1w":
		return tdxwire.CategoryWeek, nil
	case "1M":
		return tdxwire.CategoryMonth, nil
	default:
		return 0, fmt.Errorf("tdx: unsupported frequency %q", value)
	}
}

func decimal(value float64) common.Decimal {
	return common.NewDecimal(strconv.FormatFloat(value, 'f', -1, 64))
}

func barEnd(start time.Time, frequency string) time.Time {
	switch strings.TrimSpace(frequency) {
	case "1m":
		return start.Add(time.Minute).UTC()
	case "5m":
		return start.Add(5 * time.Minute).UTC()
	case "15m":
		return start.Add(15 * time.Minute).UTC()
	case "30m":
		return start.Add(30 * time.Minute).UTC()
	case "60m":
		return start.Add(time.Hour).UTC()
	case "1w":
		return start.AddDate(0, 0, 7).UTC()
	case "1M":
		return addCalendarMonth(start).UTC()
	default:
		return start.AddDate(0, 0, 1).UTC()
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
