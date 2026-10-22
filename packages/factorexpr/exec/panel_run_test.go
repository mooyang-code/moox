package exec

import (
	"math"
	"testing"
)

func TestPanelRunSettersMatchPointSetters(t *testing.T) {
	keys := []SeriesKey{{Subject: "A"}, {Subject: "B"}}
	run, err := NewPanel(6, []string{"close"}, keys)
	if err != nil {
		t.Fatal(err)
	}
	point, _ := NewPanel(6, []string{"close"}, keys)
	values := []float64{1, math.NaN(), 3}
	run.SetRun(0, 1, 2, values)
	run.MarkPresentRun(1, 2, len(values))
	for i, v := range values {
		point.Set(0, 1, 2+i, v)
		point.MarkPresent(1, 2+i)
	}
	for s := 0; s < 2; s++ {
		for tt := 0; tt < 6; tt++ {
			a, b := run.Value(0, s, tt), point.Value(0, s, tt)
			if !(a == b || (math.IsNaN(a) && math.IsNaN(b))) {
				t.Fatalf("value mismatch at series %d t %d: %v vs %v", s, tt, a, b)
			}
			if run.Present(s, tt) != point.Present(s, tt) {
				t.Fatalf("present mismatch at series %d t %d", s, tt)
			}
		}
	}
}
