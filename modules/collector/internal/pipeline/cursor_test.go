package pipeline

import "testing"

func TestCursorRejectsModifiedPayloadAndScope(t *testing.T) {
	encoded, err := EncodeCursor(Cursor{Version: 1, PlanID: "p", TaskIDsHash: "tasks", MarketID: "crypto_binance", ProviderID: "binance", SourceDatasetID: "binance_kline", UnifiedDatasetID: "spot_kline", Feed: "kline", Phase: "fetch"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeCursor(encoded, CursorScope{PlanID: "p", TaskIDsHash: "tasks", MarketID: "crypto_binance", ProviderID: "binance", SourceDatasetID: "binance_kline", UnifiedDatasetID: "spot_kline", Feed: "kline", Phase: "fetch"}); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeCursor(encoded, CursorScope{PlanID: "other"}); err == nil {
		t.Fatal("cross plan cursor accepted")
	}
}
