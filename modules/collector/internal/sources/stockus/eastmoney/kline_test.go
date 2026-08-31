package eastmoney

import "testing"

func TestSecIDRequiresUSPrefix(t *testing.T) {
	if got, err := SecID("US.AAPL"); err != nil || got != "105.AAPL" {
		t.Fatalf("SecID() = %q, %v", got, err)
	}
	if _, err := SecID("AAPL"); err == nil {
		t.Fatal("bare US symbol should be rejected")
	}
}
