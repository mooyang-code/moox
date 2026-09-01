package binance

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
)

// InstrumentSnapshotFetcher adapts Binance exchange-info to the neutral
// InstrumentFetcher contract. It deliberately returns the complete filtered
// snapshot; InstrumentPipeline owns deterministic shard selection and Storage
// membership reconciliation.
type InstrumentSnapshotFetcher struct {
	Collector *SymbolCollector
	InstType  string
}

func NewInstrumentSnapshotFetcher(instType string, routes marketdata.RouteIPProvider) *InstrumentSnapshotFetcher {
	return &InstrumentSnapshotFetcher{Collector: NewSymbolCollectorWithRoutes(routes), InstType: strings.ToUpper(strings.TrimSpace(instType))}
}

func (f *InstrumentSnapshotFetcher) Descriptor() marketdata.ProviderDescriptor {
	return NewMarketDataFetcher(f.InstType).Descriptor()
}

func (f *InstrumentSnapshotFetcher) InstrumentSpec() marketdata.InstrumentSpec {
	return marketdata.InstrumentSpec{MarketID: "crypto", InstrumentType: strings.ToLower(f.InstType), SupportsFull: true, SupportsPaging: false, HasStatus: true}
}

func (f *InstrumentSnapshotFetcher) FetchInstruments(ctx context.Context, request marketdata.InstrumentRequest) (marketdata.InstrumentSnapshot, error) {
	if f == nil || f.Collector == nil {
		return marketdata.InstrumentSnapshot{}, fmt.Errorf("binance instrument fetcher is not initialized")
	}
	if err := request.Validate(); err != nil {
		return marketdata.InstrumentSnapshot{}, err
	}
	if request.MarketID != "crypto" || request.InstrumentType != strings.ToLower(f.InstType) {
		return marketdata.InstrumentSnapshot{}, fmt.Errorf("binance: unsupported market/instrument %s/%s", request.MarketID, request.InstrumentType)
	}
	raw, err := f.Collector.fetchSymbols(ctx, &sources.CollectParams{InstType: f.InstType})
	if err != nil {
		return marketdata.InstrumentSnapshot{}, err
	}
	filtered := f.Collector.filterSymbols(raw)
	if len(filtered) == 0 {
		return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: Binance active symbol snapshot is empty", marketdata.ErrUnavailable)
	}
	items := make([]marketdata.Instrument, 0, len(filtered))
	for _, symbol := range filtered {
		if symbol == nil {
			continue
		}
		subjectID := normalizedSubjectID(symbol, strings.ToLower(f.InstType))
		providerSymbol := externalSymbol(symbol)
		if subjectID == "" || providerSymbol == "" {
			return marketdata.InstrumentSnapshot{}, fmt.Errorf("binance symbol %q has no canonical identity", symbol.Symbol)
		}
		items = append(items, marketdata.Instrument{
			SubjectID: subjectID, ProviderSymbol: providerSymbol, Name: subjectID, ExchangeID: "binance",
			Status: strings.ToLower(strings.TrimSpace(symbol.Status)), InstrumentType: strings.ToLower(f.InstType),
		})
	}
	if len(items) == 0 {
		return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: Binance active symbol snapshot is empty", marketdata.ErrUnavailable)
	}
	return marketdata.InstrumentSnapshot{MarketID: "crypto", InstrumentType: strings.ToLower(f.InstType), Items: items, Version: time.Now().UTC().Format(time.RFC3339Nano)}, nil
}

var _ marketdata.InstrumentFetcher = (*InstrumentSnapshotFetcher)(nil)
