package marketstorage

import (
	"strings"
	"testing"
)

func TestDecodeConfigAcceptsProviderMetadataAndStorageBindings(t *testing.T) {
	config := `
app:
  id: binance
  type: market
api:
  base_url: https://data-api.binance.vision
storage:
  bindings:
    spot:
      data_source_id: stock_cn
      subject_type: stock
      subject_market: stock_cn
      auth_info:
        app_id: moox-collector
        app_key: test-key
`

	var decoded sourceConfig
	if err := decodeConfig(strings.NewReader(config), &decoded); err != nil {
		t.Fatalf("decodeConfig() error = %v", err)
	}
	if got := decoded.Storage.Bindings["spot"].DataSourceID; got != "stock_cn" {
		t.Fatalf("spot data source id = %q, want stock_cn", got)
	}
}

func TestDecodeConfigRejectsUnknownTopLevelSection(t *testing.T) {
	var decoded sourceConfig
	if err := decodeConfig(strings.NewReader("unexpected: true\n"), &decoded); err == nil {
		t.Fatal("decodeConfig() error = nil, want unknown section error")
	}
}
