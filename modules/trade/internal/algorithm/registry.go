package algorithm

import (
	"errors"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/execution"
)

var ErrUnknownAlgorithm = errors.New("trade: unknown algorithm")

type Registry struct {
	split map[execution.AlgorithmDescriptor]execution.SplitAlgorithm
}

func NewRegistry() *Registry {
	return &Registry{split: map[execution.AlgorithmDescriptor]execution.SplitAlgorithm{}}
}
func (r *Registry) RegisterSplit(a execution.SplitAlgorithm) { r.split[a.Descriptor()] = a }
func (r *Registry) Split(d execution.AlgorithmDescriptor) (execution.SplitAlgorithm, error) {
	a, ok := r.split[d]
	if !ok {
		return nil, ErrUnknownAlgorithm
	}
	return a, nil
}
