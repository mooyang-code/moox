package sources

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
)

type registryFetcher struct{}

func (registryFetcher) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{ProviderID: "tdx", SourceID: "normal_7709", ProtocolVariant: marketdata.ProtocolTDXNormal, Transport: "tcp", Port: 7709, Markets: []string{"stock_cn"}, InstrumentTypes: []string{"equity"}, Frequencies: []string{"1d"}}
}
func (registryFetcher) KlineSpec() marketdata.KlineSpec {
	return marketdata.KlineSpec{MarketID: "stock_cn", InstrumentType: "equity", Frequencies: []string{"1d"}, MaxBarsPerRequest: 1, RequestTimeoutSeconds: 1}
}
func (registryFetcher) FetchKlines(context.Context, marketdata.KlineRequest) ([]marketdata.NormalizedKline, error) {
	return nil, nil
}

func TestProviderRegistryUsesSourceKey(t *testing.T) {
	registry := NewProviderRegistry()
	registration := ProviderRegistration{Descriptor: registryFetcher{}.Descriptor(), Klines: registryFetcher{}}
	if err := registry.Register(registration); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(registration); err == nil {
		t.Fatal("duplicate source key should fail")
	}
	if _, ok := registry.Lookup(marketdata.SourceKey{ProviderID: "tdx", SourceID: "normal_7709"}); !ok {
		t.Fatal("registered source not found")
	}
}
