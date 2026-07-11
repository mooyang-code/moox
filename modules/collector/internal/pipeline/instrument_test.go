package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/providers"
)

func TestInstrumentPipelinePersistsSourceBeforeWholeInstrumentResolution(t *testing.T) {
	generation := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	value := providers.ProviderInstrument{SubjectID: "BTC-USDT", ProviderID: "binance", ProviderSymbol: "BTCUSDT", ExchangeID: "BINANCE", ProductType: marketdata.ProductSpot, InstrumentType: marketdata.InstrumentSpot, Name: "BTC/USDT", Currency: "USDT", Status: "trading", EffectiveAt: generation, FetchedAt: generation, RequestID: "request"}
	store := &instrumentStoreFake{}
	pipe := InstrumentPipeline{Provider: instrumentProviderFake{value: value}, Gate: providers.StaticGate{Permit: providers.RequestPermit{Allowed: true}}, Store: store, SpaceID: "crypto_binance", SourceDatasetID: "binance_instruments", SourceDatasetIDs: []string{"binance_instruments"}, SourceDatasets: map[marketdata.ProviderID]string{"binance": "binance_instruments"}, UnifiedDatasetID: "instruments", ProviderPriority: []marketdata.ProviderID{"binance"}, Generation: generation, Now: func() time.Time { return generation.Add(time.Minute) }}
	result, err := pipe.Run(context.Background(), providers.FetchInstrumentsRequest{MarketID: "crypto_binance", ExchangeID: "BINANCE", SnapshotAt: generation})
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceRows != 1 || result.UnifiedRows != 1 || len(store.order) != 3 || store.order[0] != "source" || store.order[1] != "candidates" || store.order[2] != "unified" {
		t.Fatalf("result=%+v order=%v", result, store.order)
	}
	if store.unified.SourceDatasetID != "binance_instruments" || store.unified.SubjectID != "BTC-USDT" {
		t.Fatalf("unified=%+v", store.unified)
	}
}

type instrumentProviderFake struct{ value providers.ProviderInstrument }

func (p instrumentProviderFake) ID() marketdata.ProviderID          { return p.value.ProviderID }
func (instrumentProviderFake) Capabilities() []providers.Capability { return nil }
func (p instrumentProviderFake) FetchInstruments(context.Context, providers.RequestGate, providers.FetchInstrumentsRequest) (providers.FetchInstrumentsResult, error) {
	return providers.FetchInstrumentsResult{Instruments: []providers.ProviderInstrument{p.value}, Complete: true, RequestCount: 1}, nil
}

type instrumentStoreFake struct {
	source  []providers.ProviderInstrument
	unified providers.ResolvedInstrument
	order   []string
}

func (s *instrumentStoreFake) WriteProviderInstruments(_ context.Context, _ string, _ time.Time, values []providers.ProviderInstrument) error {
	s.order = append(s.order, "source")
	s.source = append(s.source, values...)
	return nil
}
func (s *instrumentStoreFake) InstrumentCandidates(context.Context, string, []string, string, time.Time) ([]providers.ProviderInstrument, error) {
	s.order = append(s.order, "candidates")
	return append([]providers.ProviderInstrument(nil), s.source...), nil
}
func (s *instrumentStoreFake) WriteUnifiedInstrument(_ context.Context, _ string, value providers.ResolvedInstrument) error {
	s.order = append(s.order, "unified")
	s.unified = value
	return nil
}
