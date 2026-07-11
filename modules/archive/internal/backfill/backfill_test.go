package backfill

import "testing"

func TestPlanRequiresExplicitConfirmation(t *testing.T) {
	plan := Plan{SpaceID: "crypto_binance", DatasetID: "spot_kline", SubjectID: "BTC-USDT", Freq: "1m", Start: "2026-06-01T00:00:00Z", End: "2026-07-01T00:00:00Z"}
	if plan.Confirm {
		t.Fatal("new plan unexpectedly confirmed")
	}
	if len(plan.Partitions()) != 2 {
		t.Fatalf("partitions = %v", plan.Partitions())
	}
}
