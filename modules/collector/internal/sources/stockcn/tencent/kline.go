package tencent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
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
	defaultDomain = "proxy.finance.qq.com"
	historyPath   = "/ifzqgtimg/appstock/app/newfqkline/get"
	maxRows       = 640
)

// Client implements Tencent's JSONP daily history endpoint for A-shares.
// The endpoint only exposes daily bars in this source adapter. Adjustment is
// deliberately not inferred from KlineRequest because that contract has no
// adjustment field yet.
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
		ProviderID:      "tencent",
		SourceID:        "stock_cn_http",
		ProtocolVariant: marketdata.ProtocolHTTP,
		Transport:       "https",
		Host:            c.domain(),
		Port:            443,
		Markets:         []string{"stock_cn"},
		InstrumentTypes: []string{"equity"},
		Frequencies:     []string{"1d"},
	}
}

func (c *Client) KlineSpec() marketdata.KlineSpec {
	return marketdata.KlineSpec{
		MarketID:              "stock_cn",
		InstrumentType:        "equity",
		Frequencies:           []string{"1d"},
		CompleteOHLCV:         true,
		HasAmount:             true,
		VolumeUnit:            "share",
		AmountUnit:            "cny",
		TimestampMode:         "start-label",
		SupportsRange:         true,
		MaxBarsPerRequest:     maxRows,
		RequestTimeoutSeconds: 5,
		HistoryStart:          "1990-01-01",
	}
}

func (c *Client) FetchKlines(ctx context.Context, request marketdata.KlineRequest) ([]marketdata.NormalizedKline, error) {
	if c == nil || c.HTTP == nil {
		return nil, fmt.Errorf("tencent: client is not initialized")
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if request.MarketID != "stock_cn" || request.InstrumentType != "equity" {
		return nil, fmt.Errorf("tencent: unsupported market/instrument %s/%s", request.MarketID, request.InstrumentType)
	}
	if !c.KlineSpec().SupportsFrequency(request.Frequency) {
		return nil, fmt.Errorf("tencent: unsupported frequency %q", request.Frequency)
	}
	symbol, err := NormalizeSymbol(request.ProviderSymbol)
	if err != nil {
		return nil, err
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}

	start, end := request.StartTime, request.EndTime
	if start.IsZero() {
		start = time.Date(1990, time.January, 1, 0, 0, 0, 0, time.UTC)
	}
	if end.IsZero() {
		end = now()
	}
	if end.Before(start) {
		return nil, fmt.Errorf("tencent: end_time cannot be before start_time")
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
		rows, fetchErr := c.fetchYear(ctx, symbol, year)
		if fetchErr != nil {
			return nil, fmt.Errorf("tencent: fetch %s year %d: %w", request.ProviderSymbol, year, fetchErr)
		}
		parsed, parseErr := parseRows(rows, symbol, request, location, now())
		if parseErr != nil {
			return nil, fmt.Errorf("tencent: parse %s year %d: %w", request.ProviderSymbol, year, parseErr)
		}
		for _, bar := range parsed {
			if bar.BarStart.Before(start.UTC()) || bar.BarStart.After(end.UTC()) {
				continue
			}
			key := bar.BarStart.UTC().Format(time.RFC3339Nano)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, bar)
		}
	}
	sort.SliceStable(result, func(left, right int) bool { return result[left].BarStart.Before(result[right].BarStart) })
	if request.Limit > 0 && len(result) > request.Limit {
		result = result[len(result)-request.Limit:]
	}
	return result, nil
}

func (c *Client) fetchYear(ctx context.Context, symbol string, year int) ([][]json.RawMessage, error) {
	query := url.Values{
		"_var":  {"kline_day" + strconv.Itoa(year)},
		"param": {fmt.Sprintf("%s,day,%d-01-01,%d-12-31,%d,", symbol, year, year+1, maxRows)},
		"r":     {"0.8205512681390605"},
	}
	var raw []byte
	err := c.HTTP.GetStream(ctx, c.domain(), historyPath, query, func(reader io.Reader) error {
		var err error
		raw, err = io.ReadAll(io.LimitReader(reader, 4<<20))
		return err
	})
	if err != nil {
		return nil, err
	}
	return decodeRows(raw, symbol)
}

type response struct {
	Code int                        `json:"code"`
	Data map[string]json.RawMessage `json:"data"`
	Msg  string                     `json:"msg"`
}

type securityHistory struct {
	Day    [][]json.RawMessage `json:"day"`
	QFQDay [][]json.RawMessage `json:"qfqday"`
	HFQDay [][]json.RawMessage `json:"hfqday"`
}

func decodeRows(raw []byte, symbol string) ([][]json.RawMessage, error) {
	start := bytes.IndexByte(raw, '{')
	if start < 0 {
		return nil, fmt.Errorf("JSONP payload has no JSON object")
	}
	var payload response
	decoder := json.NewDecoder(bytes.NewReader(raw[start:]))
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode JSONP payload: %w", err)
	}
	if payload.Code != 0 {
		return nil, fmt.Errorf("upstream response code %d: %s", payload.Code, strings.TrimSpace(payload.Msg))
	}
	encoded, ok := payload.Data[symbol]
	if !ok {
		return nil, fmt.Errorf("JSONP payload has no data for %s", symbol)
	}
	var history securityHistory
	if err := json.Unmarshal(encoded, &history); err != nil {
		return nil, fmt.Errorf("decode history for %s: %w", symbol, err)
	}
	for _, rows := range [][][]json.RawMessage{history.Day, history.HFQDay, history.QFQDay} {
		if len(rows) > 0 {
			return rows, nil
		}
	}
	return nil, nil
}

func parseRows(rows [][]json.RawMessage, symbol string, request marketdata.KlineRequest, location *time.Location, fetchedAt time.Time) ([]marketdata.NormalizedKline, error) {
	result := make([]marketdata.NormalizedKline, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for index, row := range rows {
		if len(row) < 9 {
			return nil, fmt.Errorf("row %d has %d fields, want at least 9", index, len(row))
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
			continue
		}
		seen[key] = struct{}{}
		values := make([]string, 5)
		for valueIndex, fieldIndex := range []int{1, 3, 4, 2, 5} {
			values[valueIndex], err = rawNumber(row[fieldIndex])
			if err != nil {
				return nil, fmt.Errorf("row %d field %d: %w", index, fieldIndex, err)
			}
		}
		amountRaw, err := rawNumberValue(row[8])
		if err != nil {
			return nil, fmt.Errorf("row %d amount: %w", index, err)
		}
		amount, err := scaleDecimal(amountRaw, 10000)
		if err != nil {
			return nil, fmt.Errorf("row %d amount: %w", index, err)
		}
		volume, err := normalizeVolume(symbol, values[4])
		if err != nil {
			return nil, fmt.Errorf("row %d volume: %w", index, err)
		}
		result = append(result, marketdata.NormalizedKline{
			SubjectID: request.SubjectID, ProviderID: "tencent", SourceID: "stock_cn_http", ProviderSymbol: request.ProviderSymbol,
			Frequency: request.Frequency, BarStart: barStart.UTC(), BarEnd: barStart.UTC().Add(24 * time.Hour),
			Open: common.NewDecimal(values[0]), High: common.NewDecimal(values[1]), Low: common.NewDecimal(values[2]), Close: common.NewDecimal(values[3]),
			Volume: common.NewDecimal(volume), Amount: marketdata.OptionalDecimal{Value: common.NewDecimal(amount), Valid: true},
			VolumeUnit: "share", AmountUnit: "cny", ProviderTime: barStart, FetchedAt: fetchedAt.UTC(),
		})
	}
	return result, nil
}

func rawString(value json.RawMessage) (string, error) {
	value = bytes.TrimSpace(value)
	if len(value) == 0 || bytes.Equal(value, []byte("null")) {
		return "", fmt.Errorf("value is empty")
	}
	if value[0] == '"' {
		var decoded string
		if err := json.Unmarshal(value, &decoded); err != nil {
			return "", err
		}
		return strings.TrimSpace(decoded), nil
	}
	return strings.TrimSpace(string(value)), nil
}

func rawNumber(value json.RawMessage) (string, error) {
	result, err := rawNumberValue(value)
	if err != nil {
		return "", err
	}
	return result, nil
}

func rawNumberValue(value json.RawMessage) (string, error) {
	result, err := rawString(value)
	if err != nil || result == "" {
		if err == nil {
			err = fmt.Errorf("value is empty")
		}
		return "", err
	}
	if _, err := strconv.ParseFloat(result, 64); err != nil {
		return "", fmt.Errorf("%q is not numeric: %w", result, err)
	}
	return result, nil
}

func parseDate(value string, location *time.Location) (time.Time, error) {
	for _, layout := range []string{"2006-01-02", "2006/01/02", "20060102"} {
		if parsed, err := time.ParseInLocation(layout, strings.TrimSpace(value), location); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported date %q", value)
}

// NormalizeSymbol follows Tencent's documented code-prefix conventions. A
// bare code is accepted only when its market can be determined unambiguously.
func NormalizeSymbol(raw string) (string, error) {
	symbol := strings.ToLower(strings.TrimSpace(raw))
	if len(symbol) > 2 && (strings.HasPrefix(symbol, "sh") || strings.HasPrefix(symbol, "sz") || strings.HasPrefix(symbol, "bj")) {
		if err := validateCode(symbol[2:]); err != nil {
			return "", fmt.Errorf("tencent: invalid symbol %q: %w", raw, err)
		}
		return symbol, nil
	}
	if parts := strings.SplitN(symbol, ".", 2); len(parts) == 2 {
		if len(parts[0]) == 2 && (parts[0] == "sh" || parts[0] == "sz" || parts[0] == "bj") {
			return NormalizeSymbol(parts[0] + parts[1])
		}
	}
	if err := validateCode(symbol); err != nil {
		return "", fmt.Errorf("tencent: symbol %q must use EXCHANGE.CODE or a market-determinable code: %w", raw, err)
	}
	switch {
	case strings.HasPrefix(symbol, "600"), strings.HasPrefix(symbol, "601"), strings.HasPrefix(symbol, "603"), strings.HasPrefix(symbol, "605"), strings.HasPrefix(symbol, "688"), strings.HasPrefix(symbol, "900"):
		return "sh" + symbol, nil
	case strings.HasPrefix(symbol, "000"), strings.HasPrefix(symbol, "001"), strings.HasPrefix(symbol, "002"), strings.HasPrefix(symbol, "003"), strings.HasPrefix(symbol, "200"), strings.HasPrefix(symbol, "300"), strings.HasPrefix(symbol, "301"):
		return "sz" + symbol, nil
	case strings.HasPrefix(symbol, "430"), strings.HasPrefix(symbol, "440"), strings.HasPrefix(symbol, "830"), strings.HasPrefix(symbol, "831"), strings.HasPrefix(symbol, "832"), strings.HasPrefix(symbol, "833"), strings.HasPrefix(symbol, "839"):
		return "bj" + symbol, nil
	default:
		return "", fmt.Errorf("tencent: cannot determine market for symbol %q", raw)
	}
}

func validateCode(code string) error {
	if len(code) != 6 {
		return fmt.Errorf("code must contain six digits")
	}
	if _, err := strconv.Atoi(code); err != nil {
		return fmt.Errorf("code must contain only digits")
	}
	return nil
}

func (c *Client) domain() string {
	if value := strings.TrimSpace(c.Domain); value != "" {
		return value
	}
	return defaultDomain
}

func normalizeVolume(symbol, raw string) (string, error) {
	if !strings.HasPrefix(symbol, "sh688") && !strings.HasPrefix(symbol, "sz399") && !strings.HasPrefix(symbol, "sh000") && !strings.HasPrefix(symbol, "sz000") {
		return scaleDecimal(raw, 100)
	}
	return raw, nil
}

func scaleDecimal(raw string, factor int64) (string, error) {
	number, ok := new(big.Rat).SetString(strings.TrimSpace(raw))
	if !ok {
		return "", fmt.Errorf("%q is not a decimal", raw)
	}
	number.Mul(number, new(big.Rat).SetInt64(factor))
	if number.IsInt() {
		return number.Num().String(), nil
	}
	value := number.FloatString(12)
	value = strings.TrimRight(strings.TrimRight(value, "0"), ".")
	return value, nil
}

var _ marketdata.KlineFetcher = (*Client)(nil)
