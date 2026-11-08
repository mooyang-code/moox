package replay

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/ledger"
)

const (
	spotA = "A|spot"
	swapA = "A|swap"
)

func TestSnapshotKeysCarryInstrumentAndMarket(t *testing.T) {
	book := ledger.NewLedger(100, 0, 0, 3)
	book.Step(ledger.Step{OK: true, Prices: map[string]float64{spotA: 10, swapA: 10}, Targets: map[string]float64{spotA: 0.5, swapA: -0.5}})
	raw, err := json.Marshal(snapshotLedger(book))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Cash      float64 `json:"cash"`
		Positions map[string]struct {
			InstrumentID string  `json:"instrument_id"`
			Market       string  `json:"market"`
			Quantity     float64 `json:"quantity"`
			Value        float64 `json:"value"`
		} `json:"positions"`
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Positions) != 2 || snapshot.Positions[spotA].Market != "spot" || snapshot.Positions[swapA].Market != "swap" || snapshot.Positions[swapA].InstrumentID != "A" {
		t.Fatalf("持仓键与字段不符：%s", raw)
	}
	if snapshot.Positions[swapA].Quantity != -5 || snapshot.Positions[swapA].Value != -50 {
		t.Fatalf("永续持仓不符：%s", raw)
	}
}

func near(t *testing.T, label string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("%s = %.9f，期望 %.9f", label, got, want)
	}
}
