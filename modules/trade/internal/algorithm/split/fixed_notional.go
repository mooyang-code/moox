package split

import (
	"context"
	"errors"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

type FixedNotional struct{}

func (FixedNotional) Descriptor() execution.AlgorithmDescriptor {
	return execution.AlgorithmDescriptor{Name: "fixed_notional", Version: "1"}
}
func (FixedNotional) Build(_ context.Context, in execution.SplitInput) ([]execution.SliceDraft, error) {
	if in.ReferencePrice.Cmp(shared.Zero()) <= 0 || in.MaxNotional.Cmp(shared.Zero()) <= 0 {
		return nil, errors.New("invalid split input")
	}
	maxQty := in.MaxNotional.Div(in.ReferencePrice)
	remain := in.Quantity
	var out []execution.SliceDraft
	for seq := 1; remain.Cmp(shared.Zero()) > 0; seq++ {
		q := maxQty
		if remain.Cmp(q) < 0 {
			q = remain
		}
		out = append(out, execution.SliceDraft{Sequence: seq, Quantity: q})
		remain = remain.Sub(q)
	}
	return out, nil
}
