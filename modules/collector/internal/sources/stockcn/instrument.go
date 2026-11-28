package stockcn

import (
	"fmt"
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
		subjectID, err = SubjectIDWithExchange(code, exchange)
		if err != nil {
			return marketdata.Instrument{}, err
		}
	default:
		subjectID, err = SubjectID(code)
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
