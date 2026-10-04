package selection

import "testing"

func TestNormalizeUsesAverageRanksForTies(t *testing.T) {
	rows := []Row{makeRow("A", "x", "1"), makeRow("B", "x", "1"), makeRow("C", "x", "3")}
	got, err := Normalize(rows, "x", "pct_rank")
	if err != nil {
		t.Fatal(err)
	}
	if got["A"] != 0.25 || got["B"] != 0.25 || got["C"] != 1 {
		t.Fatalf("average ranks = %#v", got)
	}
}

func TestNormalizeRejectsMissingAndNonFiniteValues(t *testing.T) {
	if _, err := Normalize([]Row{makeRow("A", "x", "1"), makeRow("B")}, "x", "pct_rank"); err == nil {
		t.Fatal("missing factor value was accepted")
	}
	if _, err := Normalize(nil, "x", "unknown"); err == nil {
		t.Fatal("unknown method was accepted")
	}
}
