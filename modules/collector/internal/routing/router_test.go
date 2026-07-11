package routing

import (
	"reflect"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/providers"
)

func TestRouteIsDeterministicAndFiltersCapabilityAndHealth(t *testing.T) {
	query := providers.CapabilityQuery{Feed: providers.FeedKline, ProductType: marketdata.ProductSpot, InstrumentType: marketdata.InstrumentSpot, Frequency: marketdata.FrequencyHour}
	capability := providers.Capability{Feed: providers.FeedKline, ProductType: marketdata.ProductSpot, InstrumentType: marketdata.InstrumentSpot, Frequency: marketdata.FrequencyHour}
	request := RouteRequest{ShardKey: "crypto_binance|BTC-USDT|1h|2026-07-11", Query: query, Candidates: []Candidate{
		{ProviderID: "primary", Weight: 10, Enabled: true, Health: HealthClosed, Capabilities: []providers.Capability{capability}},
		{ProviderID: "fallback", Weight: 1, Enabled: true, Health: HealthClosed, Capabilities: []providers.Capability{capability}},
		{ProviderID: "open", Weight: 1000, Enabled: true, Health: HealthOpen, Capabilities: []providers.Capability{capability}},
		{ProviderID: "wrong_frequency", Weight: 1000, Enabled: true, Health: HealthClosed, Capabilities: []providers.Capability{{Feed: providers.FeedKline, ProductType: marketdata.ProductSpot, InstrumentType: marketdata.InstrumentSpot, Frequency: marketdata.FrequencyDay}}},
	}}
	first, err := Route(request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Route(request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("route drifted: %v != %v", first, second)
	}
	if len(first) != 2 {
		t.Fatalf("route = %v, want only ready matching candidates", first)
	}
}

func TestCircuitTransitionsThroughOneHalfOpenProbe(t *testing.T) {
	now := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	policy := CircuitPolicy{FailureThreshold: 2, Cooldown: time.Minute}
	circuit := Circuit{}.TemporaryFailure(now, policy)
	if circuit.State != HealthClosed {
		t.Fatalf("first failure state = %q", circuit.State)
	}
	circuit = circuit.TemporaryFailure(now, policy)
	if circuit.State != HealthOpen {
		t.Fatalf("second failure state = %q", circuit.State)
	}
	if _, allowed := circuit.Allow(now.Add(30*time.Second), policy); allowed {
		t.Fatal("open circuit allowed before cooldown")
	}
	circuit, allowed := circuit.Allow(now.Add(time.Minute), policy)
	if !allowed || circuit.State != HealthHalfOpen || !circuit.ProbeInFlight {
		t.Fatalf("half-open probe = %+v allowed=%v", circuit, allowed)
	}
	if _, allowed := circuit.Allow(now.Add(time.Minute), policy); allowed {
		t.Fatal("second half-open probe was allowed")
	}
	if got := circuit.Success(); got.State != HealthClosed || got.ConsecutiveErrors != 0 {
		t.Fatalf("success = %+v", got)
	}
}
