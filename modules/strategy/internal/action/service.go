package action

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
)

type ResultStore interface {
	CommitResult(
		context.Context,
		store.CommitResultRequest,
	) (store.CommitResultOutcome, error)
	RecordRunnerFailure(context.Context, string, error, time.Time) error
}

type Service struct {
	Repo   ResultStore
	Engine *engine.Engine
}

// Evaluate executes one stateless Strategy against its complete input window.
// Persisting an accepted result is a separate transaction owned by the Runner
// orchestration path.
func (s *Service) Evaluate(
	ctx context.Context,
	request domain.ExecutionRequest,
	strategy domain.Strategy,
) (domain.Output, string, error) {
	if err := s.Engine.Load(ctx, strategy); err != nil {
		return domain.Output{}, "", err
	}
	return s.Engine.Run(ctx, request, strategy)
}

func (s *Service) Commit(
	ctx context.Context,
	result domain.StrategyResult,
	output domain.Output,
) (store.CommitResultOutcome, error) {
	return s.Repo.CommitResult(ctx, store.CommitResultRequest{
		Result: result,
		Output: output,
	})
}

func (s *Service) RecordFailure(
	ctx context.Context,
	runnerID string,
	failure error,
	at time.Time,
) error {
	return s.Repo.RecordRunnerFailure(ctx, runnerID, failure, at)
}
