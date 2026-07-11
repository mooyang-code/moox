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

func TestInstrumentPipelineRegistersResolvedPageAsOneBatch(t *testing.T) {
	generation := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	first := providers.ProviderInstrument{SubjectID: "BTC-USDT", ProviderID: "binance", ProviderSymbol: "BTCUSDT", ExchangeID: "BINANCE", ProductType: marketdata.ProductSpot, InstrumentType: marketdata.InstrumentSpot, EffectiveAt: generation, FetchedAt: generation}
	second := first
	second.SubjectID, second.ProviderSymbol = "ETH-USDT", "ETHUSDT"
	store, registrar := &instrumentStoreFake{}, &instrumentRegistrarFake{}
	pipe := InstrumentPipeline{Provider: instrumentProviderFake{values: []providers.ProviderInstrument{first, second}}, Gate: providers.StaticGate{Permit: providers.RequestPermit{Allowed: true}}, Store: store, Registrar: registrar, SpaceID: "crypto_binance", SourceDatasetID: "binance_instruments", UnifiedDatasetID: "instruments", ProviderPriority: []marketdata.ProviderID{"binance"}, Generation: generation}
	result, err := pipe.Run(context.Background(), providers.FetchInstrumentsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if result.UnifiedRows != 2 || registrar.calls != 1 || len(registrar.values) != 2 {
		t.Fatalf("result=%+v registrar=%+v", result, registrar)
	}
}

type instrumentProviderFake struct {
	value  providers.ProviderInstrument
	values []providers.ProviderInstrument
}

func (p instrumentProviderFake) ID() marketdata.ProviderID          { return p.value.ProviderID }
func (instrumentProviderFake) Capabilities() []providers.Capability { return nil }
func (p instrumentProviderFake) FetchInstruments(context.Context, providers.RequestGate, providers.FetchInstrumentsRequest) (providers.FetchInstrumentsResult, error) {
	values := p.values
	if len(values) == 0 {
		values = []providers.ProviderInstrument{p.value}
	}
	return providers.FetchInstrumentsResult{Instruments: values, Complete: true, RequestCount: 1}, nil
}

type instrumentRegistrarFake struct {
	calls  int
	values []providers.ResolvedInstrument
}

func (r *instrumentRegistrarFake) RegisterInstruments(_ context.Context, values []providers.ResolvedInstrument) error {
	r.calls++
	r.values = append(r.values, values...)
	return nil
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
