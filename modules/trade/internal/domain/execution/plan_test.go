package execution

import (
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"testing"
)

func TestPlanDeterministicHashAndDependencyValidation(t *testing.T) {
	d := []SliceDraft{{Sequence: 1, Quantity: shared.MustDecimal("1")}, {Sequence: 2, Quantity: shared.MustDecimal("2"), DependsOn: []int{1}}}
	a := AlgorithmDescriptor{Name: "fixed", Version: "1"}
	p1, e := NewPlan("p", "o", a, map[string]string{"x": "1"}, "snap", "rules", shared.MustDecimal("3"), d)
	if e != nil {
		t.Fatal(e)
	}
	p2, _ := NewPlan("p", "o", a, map[string]string{"x": "1"}, "snap", "rules", shared.MustDecimal("3"), d)
	if p1.InputHash != p2.InputHash {
		t.Fatal("nondeterministic")
	}
	d[0].DependsOn = []int{1}
	if _, e = NewPlan("p", "o", a, nil, "s", "r", shared.MustDecimal("3"), d); e == nil {
		t.Fatal("cycle accepted")
	}
}
