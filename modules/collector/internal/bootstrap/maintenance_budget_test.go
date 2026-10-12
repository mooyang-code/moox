package bootstrap

import "testing"

func TestMaintenanceBudgetRotatesAcrossSpacesWhenSpacesExceedBudget(t *testing.T) {
	const spaces = 5
	const total = 2
	allocated := make([]int, spaces)
	for start := 0; start < spaces; start++ {
		passTotal := 0
		for offset := 0; offset < spaces; offset++ {
			spaceIndex := (start + offset) % spaces
			budget := maintenanceBudget(total, spaces, offset)
			allocated[spaceIndex] += budget
			passTotal += budget
		}
		if passTotal != total {
			t.Fatalf("pass starting at %d received budget %d, want %d", start, passTotal, total)
		}
	}
	for index, budget := range allocated {
		if budget != total {
			t.Fatalf("space index %d received %d across rotations, want %d", index, budget, total)
		}
	}
}

func TestNextMaintenanceSpaceCursorAlwaysAdvances(t *testing.T) {
	cursor := 0
	for expected := 1; expected <= 4; expected++ {
		cursor = nextMaintenanceSpaceCursor(cursor, 4)
		if cursor != expected%4 {
			t.Fatalf("cursor after pass %d = %d, want %d", expected, cursor, expected%4)
		}
	}
	if nextMaintenanceSpaceCursor(3, 0) != 0 {
		t.Fatal("empty Space inventory must reset the maintenance cursor")
	}
}
