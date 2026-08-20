package store

import "testing"

func TestCheckEnabledOverrideRoundTrip(t *testing.T) {
	labels := `{"node_id":"node-a","service_name":"cloudnode"}`
	disabled := SetCheckEnabledOverride(labels, false)
	if enabled, ok := CheckEnabledOverride(disabled); !ok || enabled {
		t.Fatalf("disabled override = %v, %v; want false, true", enabled, ok)
	}
	enabled := SetCheckEnabledOverride(disabled, true)
	if _, ok := CheckEnabledOverride(enabled); ok {
		t.Fatalf("enabled override unexpectedly retained: %s", enabled)
	}
}
