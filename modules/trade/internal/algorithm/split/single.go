package split

import (
	"context"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/execution"
)

type Single struct{}

func (Single) Descriptor() execution.AlgorithmDescriptor {
	return execution.AlgorithmDescriptor{Name: "single", Version: "1"}
}
func (Single) Build(_ context.Context, in execution.SplitInput) ([]execution.SliceDraft, error) {
	return []execution.SliceDraft{{Sequence: 1, Quantity: in.Quantity}}, nil
}
