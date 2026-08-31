package eastmoney

import "testing"

func TestSecIDKeepsIndexExchange(t *testing.T) {
	if got, err := SecID("SH.000001"); err != nil || got != "1.000001" {
		t.Fatalf("SecID() = %q, %v", got, err)
	}
}
