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

func TestCollectorMaintenanceBudgetsHonorGlobalRowCap(t *testing.T) {
	for total := 9; total <= 50000; total++ {
		budgets := collectorMaintenanceBudgets(total)
		if got := budgets.total(); got != total {
			t.Fatalf("budgets for cap %d sum to %d", total, got)
		}
		if budgets.periodSnapshots < 1 {
			t.Fatalf("budgets for cap %d must reserve period snapshot cleanup rows: %+v", total, budgets)
		}
		if budgets.periodReadiness < 1 {
			t.Fatalf("budgets for cap %d must reserve PeriodReadiness cleanup rows: %+v", total, budgets)
		}
		if budgets.batchItems > 11500 || budgets.batches > 500 || budgets.retries > 10000 ||
			budgets.writeTargets > 12000 || budgets.instances > 10000 || budgets.runs > 3000 ||
			budgets.periodReadiness > 1000 || budgets.periodSnapshots > 1000 || budgets.periodManifests > 1000 {
			t.Fatalf("period budgets exceed process caps: %+v", budgets)
		}
	}

	budgets := collectorMaintenanceBudgets(50000)
	if budgets.runs != 3000 || budgets.periodReadiness != 1000 {
		t.Fatalf("default budget runs=%d readiness=%d, want 3000/1000: %+v", budgets.runs, budgets.periodReadiness, budgets)
	}
	if budgets.total() != 50000 {
		t.Fatalf("default maintenance budget = %d rows, want 50000", budgets.total())
	}
}
