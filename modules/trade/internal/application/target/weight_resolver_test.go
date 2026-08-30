package target

import (
	"testing"

	"github.com/mooyang-code/moox/packages/tradeeventpb"
)

func TestRequestHashCanonicalizesTargetOrderAndDecimal(t *testing.T) {
	left := &tradeeventpb.LogicalAccountTargetWeightRequested{RunnerId: "r", LogicalAccountId: "l", CommandSequence: 1, Targets: []*tradeeventpb.InstrumentWeightTarget{{InstrumentId: "B", TargetWeight: "0.50"}, {InstrumentId: "A", TargetWeight: "-0"}}}
	right := &tradeeventpb.LogicalAccountTargetWeightRequested{RunnerId: "r", LogicalAccountId: "l", CommandSequence: 1, Targets: []*tradeeventpb.InstrumentWeightTarget{{InstrumentId: "A", TargetWeight: "0"}, {InstrumentId: "B", TargetWeight: "0.5"}}}
	h1, err := RequestHash(left)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := RequestHash(right)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("hash changed after canonicalization: %s != %s", h1, h2)
	}
}
