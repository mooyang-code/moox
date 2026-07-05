package binance

import "testing"

func TestSymbolReportConcurrencyIsSerialForMetadataRefresh(t *testing.T) {
	if maxConcurrency != 1 {
		t.Fatalf("maxConcurrency = %d, want 1 to avoid concurrent metadata snapshot refresh conflicts", maxConcurrency)
	}
}
