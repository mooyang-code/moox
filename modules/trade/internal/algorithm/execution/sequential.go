package executionpolicy

import (
	"context"
	domain "github.com/mooyang-code/moox/modules/trade/internal/domain/execution"
)

type Sequential struct{}

func (Sequential) Descriptor() domain.AlgorithmDescriptor {
	return domain.AlgorithmDescriptor{Name: "sequential", Version: "1"}
}
func (Sequential) Next(_ context.Context, in domain.ExecutionState) ([]domain.ExecutionCommand, error) {
	if len(in.Ready) == 0 {
		return nil, nil
	}
	s := in.Ready[0]
	return []domain.ExecutionCommand{{SliceID: s.ID, Type: "SUBMIT"}}, nil
}
