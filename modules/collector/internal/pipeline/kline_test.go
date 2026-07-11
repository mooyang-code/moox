package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/providers"
)

func TestKlinePipelinePersistsSourceBeforeResolvingUnified(t *testing.T) {
	provider := &pipelineProvider{row: pipelineKline("binance", "10")}
	store := &pipelineStore{}
	p := KlinePipeline{Provider: provider, Gate: providers.StaticGate{Permit: providers.RequestPermit{Allowed: true}}, Store: store, Resolver: QualityResolver{Policy: QualityPolicy{AuthoritativeSingleSource: true}, Now: func() time.Time { return time.Date(2026, 7, 11, 1, 0, 0, 0, time.UTC) }}, SourceDatasetID: "binance_kline", UnifiedDatasetID: "spot_kline"}
	result, err := p.Run(context.Background(), providers.FetchKlinesRequest{MarketID: "crypto_binance", ProductType: marketdata.ProductSpot, InstrumentType: marketdata.InstrumentSpot, Frequency: marketdata.FrequencyMinute, Subjects: []providers.ProviderSubject{{SubjectID: "BTC-USDT", ProviderSymbol: "BTCUSDT"}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceRows != 1 || result.UnifiedRows != 1 {
		t.Fatalf("summary=%+v", result)
	}
	if len(store.events) != 2 || store.events[0] != "source" || store.events[1] != "unified" {
		t.Fatalf("order=%v", store.events)
	}
}

func TestKlinePipelineDoesNotResolveWhenSourceWriteFails(t *testing.T) {
	p := KlinePipeline{Provider: &pipelineProvider{row: pipelineKline("binance", "10")}, Gate: providers.StaticGate{Permit: providers.RequestPermit{Allowed: true}}, Store: &pipelineStore{sourceErr: context.DeadlineExceeded}, SourceDatasetID: "binance_kline", UnifiedDatasetID: "spot_kline"}
	if _, err := p.Run(context.Background(), providers.FetchKlinesRequest{Frequency: marketdata.FrequencyMinute}); err == nil {
		t.Fatal("source failure accepted")
	}
}

type pipelineProvider struct{ row marketdata.ProviderKline }

func (p *pipelineProvider) ID() marketdata.ProviderID            { return p.row.ProviderID }
func (p *pipelineProvider) Capabilities() []providers.Capability { return nil }
func (p *pipelineProvider) FetchKlines(context.Context, providers.RequestGate, providers.FetchKlinesRequest) (providers.FetchKlinesResult, error) {
	return providers.FetchKlinesResult{Rows: []marketdata.ProviderKline{p.row}, Complete: true, RequestCount: 1}, nil
}

type pipelineStore struct {
	events    []string
	sourceErr error
	rows      []marketdata.ProviderKline
}

func (s *pipelineStore) WriteProviderKlines(_ context.Context, _ string, rows []marketdata.ProviderKline) error {
	s.events = append(s.events, "source")
	if s.sourceErr != nil {
		return s.sourceErr
	}
	s.rows = append(s.rows, rows...)
	return nil
}
func (s *pipelineStore) Candidates(context.Context, string, marketdata.Frequency, time.Time) ([]marketdata.ProviderKline, error) {
	return s.rows, nil
}
func (s *pipelineStore) Unified(context.Context, string, marketdata.Frequency, time.Time) (*marketdata.ResolvedKline, error) {
	return nil, nil
}
func (s *pipelineStore) WriteUnifiedKline(context.Context, string, marketdata.ResolvedKline) error {
	s.events = append(s.events, "unified")
	return nil
}
