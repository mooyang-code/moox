package action

import (
	"context"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/repository"
)

type Service struct {
	Repo   *repository.Repository
	Engine *engine.Engine
}

func (s *Service) Run(ctx context.Context, t domain.Task, d domain.StrategyDefinition) (domain.Output, string, error) {
	if err := s.Engine.Load(ctx, d); err != nil {
		return domain.Output{}, "", err
	}
	out, hash, err := s.Engine.Run(ctx, t, d)
	if err != nil {
		return domain.Output{}, "", err
	}
	if t.PreviousState.StateJSON == "" {
		t.PreviousState.StateJSON = "{}"
	}
	if err := s.Repo.Commit(ctx, t, out, hash); err != nil {
		return domain.Output{}, "", err
	}
	return out, hash, nil
}
