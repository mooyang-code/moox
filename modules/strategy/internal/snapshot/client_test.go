package snapshot

import "testing"

func TestNormalizeStableHash(t *testing.T) {
	a, ha, _ := Normalize(Input{Data: []map[string]any{{"close": 1}}, Revision: "r", Cutoff: "t"})
	b, hb, _ := Normalize(Input{Data: []map[string]any{{"close": 1}}, Revision: "r", Cutoff: "t"})
	if len(a) != len(b) || ha != hb {
		t.Fatal()
	}
}
