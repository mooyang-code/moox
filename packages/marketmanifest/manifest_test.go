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
	spot, ok := catalog.Lookup("crypto", "spot")
	if !ok || !spot.SupportsDataset("spot_kline_1h") || !spot.SupportsDataset("binance_spot_kline_1m") {
		t.Fatalf("crypto spot manifest does not expose both canonical datasets: %+v, found=%v", spot, ok)
	}
	if !spot.SupportsDatasetFrequency("binance_spot_kline_1m", "1m") || spot.SupportsDatasetFrequency("binance_spot_kline_1m", "1H") {
		t.Fatalf("crypto spot dataset/frequency mapping is not isolated: %+v", spot.DatasetFrequencies)
	}
	if !spot.SupportsDatasetFrequency("spot_kline_1h", "1H") || spot.SupportsDatasetFrequency("spot_kline_1h", "1m") {
		t.Fatalf("crypto aggregate dataset/frequency mapping is not isolated: %+v", spot.DatasetFrequencies)
	}
}

func TestManifestRejectsIncompleteDatasetFrequencyMapping(t *testing.T) {
	manifest := Manifest{
		MarketID: "crypto", InstrumentType: "spot", DatasetID: "bars", DatasetIDs: []string{"bars_1h"},
		DatasetFrequencies: map[string][]string{"bars": {"1m"}}, Timezone: "UTC", Frequencies: []string{"1m", "1H"},
		Sources: []SourceRef{{ProviderID: "binance", SourceID: "spot", ProtocolVariant: "http", Transport: "https", Port: 443, Frequencies: []string{"1m", "1H"}}},
	}
	if err := manifest.Validate(); err == nil {
		t.Fatal("expected missing dataset frequency mapping error")
	}
}

func TestManifestDistinguishesMinuteAndMonthFrequencies(t *testing.T) {
	manifest := Manifest{
		MarketID: "stock_cn", InstrumentType: "equity", DatasetID: "bars", Timezone: "Asia/Shanghai", Frequencies: []string{"1m", "1M"},
		Sources: []SourceRef{{ProviderID: "tdx", SourceID: "normal", ProtocolVariant: "tdx", Transport: "tcp", Port: 7709, Frequencies: []string{"1m", "1M"}}},
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("minute and month frequencies must remain distinct: %v", err)
	}
}

func TestDefaultCatalogKeepsUnverifiedIndexSourcesCatalogOnly(t *testing.T) {
	catalog, err := DefaultCatalog()
	if err != nil {
		t.Fatal(err)
	}
	manifest, ok := catalog.Lookup("stock_cn", "index")
	if !ok {
		t.Fatal("index manifest was not found")
	}
	for _, key := range []string{"cni/index_cni_http", "sw/index_sw_http"} {
		var found *SourceRef
		for index := range manifest.Sources {
			candidate := &manifest.Sources[index]
			if candidate.ProviderID+"/"+candidate.SourceID == key {
				found = candidate
				break
			}
		}
		if found == nil {
			t.Fatalf("source %s was not found", key)
		}
		if found.IsEnabled() || found.Status != SourceCatalogOnly {
			t.Fatalf("source %s must remain catalog-only: %+v", key, *found)
		}
	}
}

func TestDefaultCatalogKeepsUnverifiedMarketSourcesCatalogOnly(t *testing.T) {
	catalog, err := DefaultCatalog()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		market     string
		instrument string
		sourceKeys []string
	}{
		{market: "stock_cn", instrument: "equity", sourceKeys: []string{"sina/stock_cn_http", "sina/stock_cn_minute_http", "ths/daily_http"}},
		{market: "stock_hk", instrument: "equity", sourceKeys: []string{"sina/stock_hk_http", "tdx/ex_classic_7727", "tdx/ex_mac_7727"}},
		{market: "stock_us", instrument: "equity", sourceKeys: []string{"sina/stock_us_http", "tdx/ex_classic_7727", "tdx/ex_mac_7727"}},
	}
	for _, testCase := range cases {
		manifest, ok := catalog.Lookup(testCase.market, testCase.instrument)
		if !ok {
			t.Fatalf("manifest %s/%s was not found", testCase.market, testCase.instrument)
		}
		for _, key := range testCase.sourceKeys {
			var found *SourceRef
			for index := range manifest.Sources {
				candidate := &manifest.Sources[index]
				if candidate.ProviderID+"/"+candidate.SourceID == key {
					found = candidate
					break
				}
			}
			if found == nil {
				t.Fatalf("source %s was not found in %s/%s", key, testCase.market, testCase.instrument)
			}
			if found.IsEnabled() || found.Status != SourceCatalogOnly {
				t.Fatalf("source %s must remain catalog-only: %+v", key, *found)
			}
		}
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
