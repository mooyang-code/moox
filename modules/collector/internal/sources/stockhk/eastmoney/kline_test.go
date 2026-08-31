package eastmoney

import "testing"

func TestSecIDPreservesHKLeadingZero(t *testing.T) {
	if got, err := SecID("HK.00700"); err != nil || got != "116.00700" {
		t.Fatalf("SecID() = %q, %v", got, err)
	}
}
