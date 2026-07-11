package execution

import (
	"context"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
)

type Request struct {
	ExecutionID, GroupID, Mode, IdempotencyKey string
	Targets                                    []domain.TargetWeight
}
type Result struct {
	ExecutionID, Status string
	Error               string
}
type Port interface {
	Submit(context.Context, Request) (Result, error)
	Inspect(context.Context, string) (Result, error)
}
