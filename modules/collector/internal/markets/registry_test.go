package markets

import (
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/packages/marketmanifest"
)

type testModule struct{ descriptor Descriptor }

func (m testModule) Descriptor() Descriptor   { return m.descriptor }
func (m testModule) Universe() UniversePolicy { return nil }
func (m testModule) Calendar() CalendarPolicy { return nil }
func (m testModule) Symbols() SymbolPolicy    { return nil }
func (m testModule) Routing() RoutingPolicy   { return nil }
func (m testModule) Quality() QualityPolicy   { return nil }
func (m testModule) Coverage() CoveragePolicy { return nil }

func TestRegistryUsesExactMarketIDAndRejectsDuplicates(t *testing.T) {
	registry := NewRegistry()
	module := testModule{descriptor: Descriptor{MarketID: marketdata.MarketID("stock_cn"), SpaceID: "stock_cn"}}
	if err := registry.Register(module); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, ok := registry.Lookup(marketdata.MarketID("stock_cn")); !ok {
		t.Fatal("registered market was not found")
	}
	if _, ok := registry.Lookup(marketdata.MarketID("stock")); ok {
		t.Fatal("registry must not infer IDs by prefix")
	}
	if err := registry.Register(module); err == nil {
		t.Fatal("duplicate market registration should fail")
	}
}

func TestDescriptorFromManifest(t *testing.T) {
	descriptor, err := DescriptorFromManifest(marketmanifest.Manifest{
		MarketID: "crypto_binance", SpaceID: "crypto_binance", AssetClass: "crypto", Timezone: "UTC",
	})
	if err != nil {
		t.Fatalf("DescriptorFromManifest: %v", err)
	}
	if descriptor.MarketID != "crypto_binance" || descriptor.SpaceID != "crypto_binance" {
		t.Fatalf("unexpected descriptor: %+v", descriptor)
	}
}
