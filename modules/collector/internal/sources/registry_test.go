package sources

import (
	"context"
	"testing"
)

type noopCollector struct{}

func (noopCollector) Source() string { return "binance" }

func (noopCollector) DataType() string { return "kline" }

func (noopCollector) Collect(context.Context, *CollectParams) error { return nil }

func TestRegistryRoutesByMarket(t *testing.T) {
	r := &CollectorRegistry{collectors: make(map[string]*CollectorDescriptor)}
	collector := noopCollector{}
	for _, market := range []string{"spot", "swap"} {
		if err := r.Register(&CollectorDescriptor{
			Source:    "binance",
			Market:    market,
			DataType:  "kline",
			Collector: collector,
		}); err != nil {
			t.Fatalf("Register(%s) error = %v", market, err)
		}
	}

	if _, err := r.Get("BINANCE", "SPOT", "KLINE"); err != nil {
		t.Fatalf("Get spot error = %v", err)
	}
	if _, err := r.Get("binance", "swap", "kline"); err != nil {
		t.Fatalf("Get swap error = %v", err)
	}
}
