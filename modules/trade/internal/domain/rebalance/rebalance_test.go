package rebalance

import (
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"testing"
)

func TestPlannerSellReduceBeforeIncreaseAndZeroCloses(t *testing.T) {
	legs, e := (Planner{}).Build([]Target{{"BTC", shared.Zero()}, {"ETH", shared.MustDecimal("2")}}, []Current{{"BTC", shared.MustDecimal("1")}, {"ETH", shared.MustDecimal("1")}})
	if e != nil {
		t.Fatal(e)
	}
	if len(legs) != 2 || legs[0].Action != Close || !legs[0].ReduceOnly || legs[1].Action != Increase || len(legs[1].DependsOn) != 1 {
		t.Fatalf("%+v", legs)
	}
}

func TestPlannerSplitsReverseAndFullModeClosesMissingTarget(t *testing.T) {
	legs, e := (Planner{}).Build([]Target{{"ETH", shared.MustDecimal("-5")}}, []Current{{"ETH", shared.MustDecimal("10")}, {"BTC", shared.MustDecimal("2")}})
	if e != nil {
		t.Fatal(e)
	}
	if len(legs) != 3 {
		t.Fatalf("%+v", legs)
	}
	if legs[0].Action != Close || legs[0].Quantity.String() != "2" || legs[1].Action != Close || legs[1].Quantity.String() != "10" || legs[2].Action != Open || legs[2].Quantity.String() != "5" || len(legs[2].DependsOn) != 2 {
		t.Fatalf("%+v", legs)
	}
}
