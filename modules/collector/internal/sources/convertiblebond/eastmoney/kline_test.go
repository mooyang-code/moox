package eastmoney

import "testing"

func TestSecIDRejectsOrdinaryStockWithoutExchange(t *testing.T) {
	if _, err := SecID("113001"); err == nil {
		t.Fatal("convertible bond symbol without exchange should be rejected")
	}
}
