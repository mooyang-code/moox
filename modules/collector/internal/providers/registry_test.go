package providers

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
)

func TestRegistryRejectsDuplicateAndFiltersCapability(t *testing.T) {
	registry := NewRegistry()
	provider := &testProvider{id: "first", caps: []Capability{{Feed: FeedKline, ProductType: marketdata.ProductSpot, InstrumentType: marketdata.InstrumentSpot, Frequency: marketdata.FrequencyMinute}}}
	if err := registry.Register(provider); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(provider); err == nil {
		t.Fatal("duplicate registration succeeded")
	}
	if _, err := registry.Kline("first", CapabilityQuery{Feed: FeedKline, ProductType: marketdata.ProductSpot, InstrumentType: marketdata.InstrumentSpot, Frequency: marketdata.FrequencyMinute}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Kline("first", CapabilityQuery{Feed: FeedKline, ProductType: marketdata.ProductSpot, InstrumentType: marketdata.InstrumentSpot, Frequency: marketdata.FrequencyDay}); !IsKind(err, ErrorUnsupported) {
		t.Fatalf("want typed unsupported, got %v", err)
	}
}

func TestFakeProviderDoesNotFetchWhenPermitIsDenied(t *testing.T) {
	provider := &testProvider{id: "first", caps: []Capability{{Feed: FeedKline, ProductType: marketdata.ProductSpot, InstrumentType: marketdata.InstrumentSpot, Frequency: marketdata.FrequencyMinute}}}
	gate := StaticGate{Permit: RequestPermit{Allowed: false, DenialReason: "quota"}}
	_, err := provider.FetchKlines(context.Background(), gate, FetchKlinesRequest{MarketID: "crypto_binance", ProductType: marketdata.ProductSpot, InstrumentType: marketdata.InstrumentSpot, Frequency: marketdata.FrequencyMinute, Subjects: []ProviderSubject{{SubjectID: "BTC-USDT", ProviderSymbol: "BTCUSDT"}}, StartTime: time.Now().UTC(), EndTime: time.Now().UTC()})
	if !IsKind(err, ErrorRateLimited) || provider.calls != 0 {
		t.Fatalf("denied permit must prevent network call, calls=%d err=%v", provider.calls, err)
	}
}

type testProvider struct {
	id    marketdata.ProviderID
	caps  []Capability
	calls int
}

func (p *testProvider) ID() marketdata.ProviderID  { return p.id }
func (p *testProvider) Capabilities() []Capability { return p.caps }
func (p *testProvider) FetchKlines(ctx context.Context, gate RequestGate, req FetchKlinesRequest) (FetchKlinesResult, error) {
	permit, err := gate.BeforeRequest(ctx, RequestMeta{ProviderID: p.id, EndpointClass: "test", RequestCost: 1})
	if err != nil {
		return FetchKlinesResult{}, err
	}
	if !permit.Allowed {
		return FetchKlinesResult{}, NewError(ErrorRateLimited, permit.DenialReason, nil)
	}
	p.calls++
	return FetchKlinesResult{Complete: true, RequestCount: 1}, nil
}
