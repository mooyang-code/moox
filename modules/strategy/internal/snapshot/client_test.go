package snapshot

import "testing"

func TestNormalizeStableHash(t *testing.T) {
	a, ha, _ := Normalize(Input{Data: []map[string]any{{"close": 1}}, Revision: "r", Cutoff: "t"})
	b, hb, _ := Normalize(Input{Data: []map[string]any{{"close": 1}}, Revision: "r", Cutoff: "t"})
	if len(a) != len(b) || ha != hb {
		t.Fatal()
	}
}

func TestCaptureReturnsDetachedData(t *testing.T) {
	rows := []map[string]any{{"close": 1.0}}
	s, err := Capture(Input{Data: rows, Revision: "r", Cutoff: "2026-01-01T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	copyRows := s.Data()
	copyRows[0]["close"] = 99.0
	if got := s.Data()[0]["close"]; got != float64(1) {
		t.Fatalf("snapshot was mutable: %v", got)
	}
}
