package algorithm_test

import (
	"context"
	"github.com/mooyang-code/moox/modules/trade/internal/algorithm"
	"github.com/mooyang-code/moox/modules/trade/internal/algorithm/split"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"testing"
)

func TestFixedNotionalPreservesQuantityAndRegistryVersion(t *testing.T) {
	r := algorithm.NewRegistry()
	r.RegisterSplit(split.FixedNotional{})
	a, err := r.Split(execution.AlgorithmDescriptor{Name: "fixed_notional", Version: "1"})
	if err != nil {
		t.Fatal(err)
	}
	xs, err := a.Build(context.Background(), execution.SplitInput{Quantity: shared.MustDecimal("10"), ReferencePrice: shared.MustDecimal("3"), MaxNotional: shared.MustDecimal("9")})
	if err != nil {
		t.Fatal(err)
	}
	sum := shared.Zero()
	for _, x := range xs {
		sum = sum.Add(x.Quantity)
	}
	if sum.String() != "10" || len(xs) != 4 {
		t.Fatalf("sum=%s n=%d", sum.String(), len(xs))
	}
	if _, err = r.Split(execution.AlgorithmDescriptor{Name: "fixed_notional", Version: "2"}); err == nil {
		t.Fatal("unknown version accepted")
	}
}
