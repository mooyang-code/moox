package action

import (
	"context"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
)

type Service struct {
	Repo   *store.Repository
	Engine *engine.Engine
}

func (s *Service) Run(ctx context.Context, t domain.Task, d domain.StrategyDefinition) (domain.Output, string, error) {
	out, hash, err := s.Evaluate(ctx, t, d)
	if err != nil {
		return domain.Output{}, "", err
	}
	if err := s.Commit(ctx, t, out, hash); err != nil {
		return domain.Output{}, "", err
	}
	return out, hash, nil
}

// Evaluate executes a strategy without changing state. Backtests, dry runs,
// and the RPC commit=false path all use this method so they share the exact
// Python contract with live execution.
func (s *Service) Evaluate(ctx context.Context, t domain.Task, d domain.StrategyDefinition) (domain.Output, string, error) {
	if err := s.Engine.Load(ctx, d); err != nil {
		return domain.Output{}, "", err
	}
	out, hash, err := s.Engine.Run(ctx, t, d)
	if err != nil {
		return domain.Output{}, "", err
	}
	return out, hash, nil
}

func (s *Service) Commit(ctx context.Context, t domain.Task, out domain.Output, hash string) error {
	if t.PreviousState.StateJSON == "" {
		t.PreviousState.StateJSON = "{}"
	}
	if err := s.Repo.Commit(ctx, t, out, hash); err != nil {
		return err
	}
	return nil
}
