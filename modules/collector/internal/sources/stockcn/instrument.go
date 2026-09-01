package stockcn

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
)

type instrumentSnapshotBuilder struct {
	providerID string
	marketID   string
	fetchedAt  time.Time

	pageCount   int
	seen        map[string]struct{}
	instruments []marketdata.Instrument
	counts      map[string]int
}

func NewInstrumentSnapshotBuilder(providerID, marketID string, fetchedAt time.Time) *instrumentSnapshotBuilder {
	return &instrumentSnapshotBuilder{
		providerID: providerID,
		marketID:   marketID,
		fetchedAt:  fetchedAt.UTC(),
		seen:       make(map[string]struct{}),
		counts:     make(map[string]int),
	}
}

func (b *instrumentSnapshotBuilder) Add(instrument marketdata.Instrument) error {
	if strings.TrimSpace(instrument.SubjectID) == "" {
		return fmt.Errorf("%w: instrument subject_id is required", marketdata.ErrProtocol)
	}
	if strings.TrimSpace(instrument.ProviderSymbol) == "" {
		return fmt.Errorf("%w: instrument provider_symbol is required", marketdata.ErrProtocol)
	}
	if strings.TrimSpace(instrument.Exchange) == "" {
		return fmt.Errorf("%w: instrument exchange is required", marketdata.ErrProtocol)
	}
	if _, ok := b.seen[instrument.SubjectID]; ok {
		return fmt.Errorf("%w: duplicate instrument subject_id %q", marketdata.ErrProtocol, instrument.SubjectID)
	}
	b.seen[instrument.SubjectID] = struct{}{}
	b.instruments = append(b.instruments, instrument)
	b.counts[instrument.Exchange]++
	return nil
}

func (b *instrumentSnapshotBuilder) NextPage() {
	b.pageCount++
}

func (b *instrumentSnapshotBuilder) Snapshot() (marketdata.InstrumentSnapshot, error) {
	if len(b.instruments) == 0 {
		return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: empty instrument snapshot", marketdata.ErrProtocol)
	}
	snapshot := marketdata.InstrumentSnapshot{
		SnapshotID:     marketdata.SnapshotID(b.providerID, b.marketID, b.fetchedAt),
		SourceProvider: b.providerID,
		MarketID:       b.marketID,
		FetchedAt:      b.fetchedAt,
		Complete:       true,
		PageCount:      b.pageCount,
		ExchangeCounts: b.counts,
		Instruments:    append([]marketdata.Instrument(nil), b.instruments...),
	}
	if err := marketdata.ValidateInstrumentSnapshot(snapshot); err != nil {
		return marketdata.InstrumentSnapshot{}, err
	}
	return snapshot, nil
}

func DecodeJSONObject(body []byte) (map[string]any, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("%w: %v", marketdata.ErrProtocol, err)
	}
	return payload, nil
}

func valueAt(root any, path ...string) any {
	current := root
	for _, key := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current, ok = object[key]
		if !ok {
			return nil
		}
	}
	return current
}

func ObjectAt(root map[string]any, path ...string) map[string]any {
	value := valueAt(root, path...)
	object, _ := value.(map[string]any)
	return object
}

func SliceOfObjectsAt(root map[string]any, path ...string) []map[string]any {
	value := valueAt(root, path...)
	rawItems, ok := value.([]any)
	if !ok {
		return nil
	}
	items := make([]map[string]any, 0, len(rawItems))
	for _, rawItem := range rawItems {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		items = append(items, item)
	}
	return items
}

func StringField(object map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := object[key]; ok {
			if text := toString(value); text != "" {
				return text
			}
		}
	}
	return ""
}

func IntField(object map[string]any, keys ...string) (int, bool) {
	for _, key := range keys {
		value, ok := object[key]
		if !ok {
			continue
		}
		switch v := value.(type) {
		case int:
			return v, true
		case int32:
			return int(v), true
		case int64:
			return int(v), true
		case float64:
			return int(v), true
		case json.Number:
			n, err := v.Int64()
			if err == nil {
				return int(n), true
			}
		case string:
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err == nil {
				return n, true
			}
		}
	}
	return 0, false
}

func BoolField(object map[string]any, keys ...string) (bool, bool) {
	for _, key := range keys {
		value, ok := object[key]
		if !ok {
			continue
		}
		switch v := value.(type) {
		case bool:
			return v, true
		case float64:
			return v != 0, true
		case int:
			return v != 0, true
		case int64:
			return v != 0, true
		case json.Number:
			n, err := v.Int64()
			if err == nil {
				return n != 0, true
			}
		case string:
			switch strings.ToLower(strings.TrimSpace(v)) {
			case "true", "1", "yes", "y":
				return true, true
			case "false", "0", "no", "n":
				return false, true
			}
		}
	}
	return false, false
}

func toString(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case json.Number:
		return v.String()
	case float64:
		if math.Trunc(v) == v {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	case int32:
		return strconv.FormatInt(int64(v), 10)
	case int64:
		return strconv.FormatInt(v, 10)
	case bool:
		if v {
			return "true"
		}
		return "false"
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func normalizeExchange(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "1", "XSHG", "SH", "SSE", "SHANGHAI", "上海":
		return "XSHG"
	case "0", "XSHE", "SZ", "SZSE", "SHENZHEN", "深圳":
		return "XSHE"
	case "2", "XBSE", "BJ", "BSE", "BEIJING", "北京":
		return "XBSE"
	default:
		return ""
	}
}

func PageLimit(data map[string]any, pageSize int) int {
	if limit, ok := IntField(data, "pagecount", "page_count", "total_pages", "totalpage", "pages"); ok && limit > 0 {
		return limit
	}
	if total, ok := IntField(data, "total", "count", "total_count", "totalnum", "total_nums"); ok && total > 0 && pageSize > 0 {
		return (total + pageSize - 1) / pageSize
	}
	return 0
}

func ItemSlice(data map[string]any, keys ...string) []map[string]any {
	for _, key := range keys {
		if items := SliceOfObjectsAt(data, key); len(items) > 0 {
			return items
		}
	}
	return nil
}

func InstrumentFromFields(code, symbol, exchangeValue, name, status string) (marketdata.Instrument, error) {
	code = strings.TrimSpace(code)
	symbol = strings.TrimSpace(symbol)
	exchange := normalizeExchange(exchangeValue)

	var (
		subjectID string
		err       error
	)
	switch {
	case symbol != "":
		subjectID, err = DecodeProviderSymbol(symbol)
		if err != nil {
			return marketdata.Instrument{}, err
		}
		if exchange != "" && !strings.HasSuffix(subjectID, "."+exchange) {
			return marketdata.Instrument{}, fmt.Errorf("%w: symbol %q does not match exchange %q", marketdata.ErrUnsupportedSymbol, symbol, exchange)
		}
	case code != "" && exchange != "":
		subjectID, err = CanonicalSubjectIDWithExchange(code, exchange)
		if err != nil {
			return marketdata.Instrument{}, err
		}
	default:
		subjectID, err = CanonicalSubjectID(code)
		if err != nil {
			return marketdata.Instrument{}, err
		}
	}
	if exchange == "" {
		parts := strings.Split(subjectID, ".")
		exchange = parts[len(parts)-1]
	}
	providerSymbol, err := ProviderSymbol(subjectID)
	if err != nil {
		return marketdata.Instrument{}, err
	}
	status = normalizeInstrumentStatus(status)
	return marketdata.Instrument{
		SubjectID:      subjectID,
		ProviderSymbol: providerSymbol,
		Exchange:       exchange,
		Name:           strings.TrimSpace(name),
		Status:         strings.TrimSpace(status),
	}, nil
}

func normalizeInstrumentStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "1", "active", "listed", "normal", "trade":
		return "active"
	case "0", "inactive", "suspended", "halted":
		return "inactive"
	case "2", "delisted", "removed":
		return "delisted"
	default:
		return strings.TrimSpace(status)
	}
}
