package test

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/pipeline"
	"github.com/mooyang-code/moox/modules/collector/internal/providers"
)

// This is a module-level E2E test: a Provider result travels through the same
// source-first Pipeline and whole-candle resolver used by an SCF JobItem.
func TestMarketPipelineE2E_SourceFirstFallbackAndIdempotentRetry(t *testing.T) {
	store := newE2EStore()
	primary := &e2EProvider{id: "primary", rows: []marketdata.ProviderKline{e2EKline("primary", "10")}}
	pipe := pipeline.KlinePipeline{Provider: primary, Gate: providers.StaticGate{Permit: providers.RequestPermit{Allowed: true}}, Store: store, SpaceID: "crypto_binance", SourceDatasetID: "primary_kline", SourceDatasetIDs: []string{"primary_kline", "fallback_kline"}, SourceDatasets: map[marketdata.ProviderID]string{"primary": "primary_kline", "fallback": "fallback_kline"}, UnifiedDatasetID: "spot_kline", Resolver: pipeline.QualityResolver{Policy: pipeline.QualityPolicy{ProviderPriority: []marketdata.ProviderID{"primary", "fallback"}, AuthoritativeSingleSource: true}, Now: func() time.Time { return fixedE2ETime }}}
	req := providers.FetchKlinesRequest{MarketID: "crypto_binance", ProductType: marketdata.ProductSpot, InstrumentType: marketdata.InstrumentSpot, Frequency: marketdata.FrequencyHour, Subjects: []providers.ProviderSubject{{SubjectID: "BTC-USDT", ProviderSymbol: "BTCUSDT"}}}
	if summary, err := pipe.Run(context.Background(), req); err != nil || summary.SourceRows != 1 || summary.UnifiedRows != 1 {
		t.Fatalf("first run summary=%+v err=%v", summary, err)
	}
	if len(store.source) != 1 || len(store.unified) != 1 || store.unified[0].ProviderID != "primary" {
		t.Fatalf("source-first write failed: %+v", store)
	}
	if _, err := pipe.Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(store.unified) != 2 || store.unified[1].Revision != 1 {
		t.Fatalf("retry must preserve revision: %+v", store.unified)
	}
	fallback := &e2EProvider{id: "fallback", rows: []marketdata.ProviderKline{e2EKline("fallback", "11")}}
	pipe.Provider = fallback
	pipe.SourceDatasetID = "fallback_kline"
	if _, err := pipe.Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := store.unified[len(store.unified)-1]; got.ProviderID != "primary" || got.Close.String() != "10" || got.SourceDatasetID != "primary_kline" {
		t.Fatalf("fallback must not mix or replace primary whole candle: %+v", got)
	}
}

var fixedE2ETime = time.Date(2026, 7, 11, 1, 0, 0, 0, time.UTC)

func e2EKline(id, close string) marketdata.ProviderKline {
	c := marketdata.MustDecimal(close)
	volume := marketdata.MustDecimal("2")
	amount := marketdata.MustDecimal("20")
	return marketdata.ProviderKline{SubjectID: "BTC-USDT", ProviderID: marketdata.ProviderID(id), ProviderSymbol: "BTCUSDT", Frequency: marketdata.FrequencyHour, DataTime: fixedE2ETime.Add(-time.Hour), CloseTime: fixedE2ETime, TradeDate: "2026-07-11", FeedScope: "spot", VolumeUnit: "base", AmountUnit: "quote", Open: marketdata.MustDecimal("9"), High: marketdata.MustDecimal("12"), Low: marketdata.MustDecimal("8"), Close: c, Volume: &volume, Amount: &amount, ProviderTimestamp: fixedE2ETime, FetchedAt: fixedE2ETime, RequestID: string(id), Closed: true}
}

type e2EProvider struct {
	id   marketdata.ProviderID
	rows []marketdata.ProviderKline
}

func (p *e2EProvider) ID() marketdata.ProviderID          { return p.id }
func (*e2EProvider) Capabilities() []providers.Capability { return nil }
func (p *e2EProvider) FetchKlines(context.Context, providers.RequestGate, providers.FetchKlinesRequest) (providers.FetchKlinesResult, error) {
	return providers.FetchKlinesResult{Rows: p.rows, Complete: true, RequestCount: 1}, nil
}

type e2EStore struct {
	source  []marketdata.ProviderKline
	unified []marketdata.ResolvedKline
}

func newE2EStore() *e2EStore { return &e2EStore{} }
func (s *e2EStore) WriteProviderKlines(_ context.Context, _ string, rows []marketdata.ProviderKline) error {
	s.source = append(s.source, rows...)
	return nil
}
func (s *e2EStore) Candidates(context.Context, string, []string, string, marketdata.Frequency, time.Time) ([]marketdata.ProviderKline, error) {
	return append([]marketdata.ProviderKline(nil), s.source...), nil
}
func (s *e2EStore) Unified(_ context.Context, _, _, _ string, _ marketdata.Frequency, _ time.Time) (*marketdata.ResolvedKline, error) {
	if len(s.unified) == 0 {
		return nil, nil
	}
	value := s.unified[len(s.unified)-1]
	return &value, nil
}
func (s *e2EStore) WriteUnifiedKline(_ context.Context, _ string, value marketdata.ResolvedKline) error {
	s.unified = append(s.unified, value)
	return nil
}
