package marketmanifest

import "testing"

func TestDefaultCatalogHasDistinctCanonicalDatasets(t *testing.T) {
	catalog, err := DefaultCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := catalog.Lookup("stock_cn", "index"); !ok || got.DatasetID != "stock_cn_index_kline" {
		t.Fatalf("index manifest = %+v, found=%v", got, ok)
	}
	if got, ok := catalog.Lookup("stock_cn", "convertible_bond"); !ok || got.DatasetID != "stock_cn_convertible_bond_kline" {
		t.Fatalf("convertible bond manifest = %+v, found=%v", got, ok)
	}
}

func TestManifestRejectsDuplicateSource(t *testing.T) {
	source := SourceRef{ProviderID: "tdx", SourceID: "normal_7709", ProtocolVariant: "tdx_normal", Transport: "tcp", Port: 7709, Frequencies: []string{"1d"}}
	manifest := Manifest{MarketID: "stock_cn", InstrumentType: "equity", DatasetID: "bars", Timezone: "Asia/Shanghai", Frequencies: []string{"1d"}, Sources: []SourceRef{source, source}}
	if err := manifest.Validate(); err == nil {
		t.Fatal("expected duplicate source error")
	}
}

func TestManifestRejectsDuplicateFrequency(t *testing.T) {
	manifest := Manifest{MarketID: "stock_cn", InstrumentType: "equity", DatasetID: "bars", Timezone: "Asia/Shanghai", Frequencies: []string{"1d", "1d"}, Sources: []SourceRef{{ProviderID: "tdx", SourceID: "normal_7709", ProtocolVariant: "tdx_normal", Transport: "tcp", Port: 7709, Frequencies: []string{"1d"}}}}
	if err := manifest.Validate(); err == nil {
		t.Fatal("expected duplicate frequency error")
	}
}

func TestManifestRejectsUnknownSourceStatus(t *testing.T) {
	manifest := Manifest{MarketID: "stock_cn", InstrumentType: "equity", DatasetID: "bars", Timezone: "Asia/Shanghai", Frequencies: []string{"1d"}, Sources: []SourceRef{{ProviderID: "sina", SourceID: "bars", ProtocolVariant: "http", Transport: "https", Port: 443, Frequencies: []string{"1d"}, Status: "experimental"}}}
	if err := manifest.Validate(); err == nil {
		t.Fatal("expected invalid source status error")
	}
}

func TestCatalogOnlySourceIsNotEnabled(t *testing.T) {
	source := SourceRef{ProviderID: "sina", SourceID: "bars", ProtocolVariant: "http", Transport: "https", Port: 443, Frequencies: []string{"1d"}, Status: SourceCatalogOnly}
	if source.IsEnabled() {
		t.Fatal("catalog-only source must not be enabled")
	}
}

func TestCatalogReturnsDeepCopiesOfNestedSourceFrequencies(t *testing.T) {
	manifest := Manifest{MarketID: "stock_cn", InstrumentType: "equity", DatasetID: "bars", Timezone: "Asia/Shanghai", Frequencies: []string{"1d"}, Sources: []SourceRef{{ProviderID: "eastmoney", SourceID: "bars", ProtocolVariant: "http", Transport: "https", Port: 443, Frequencies: []string{"1d"}}}}
	catalog, err := NewCatalog(manifest)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := catalog.Lookup("stock_cn", "equity")
	if !ok {
		t.Fatal("manifest was not found")
	}
	got.Sources[0].Frequencies[0] = "1m"
	again, ok := catalog.Lookup("stock_cn", "equity")
	if !ok || again.Sources[0].Frequencies[0] != "1d" {
		t.Fatalf("catalog was mutated through Lookup result: %+v", again)
	}
}
