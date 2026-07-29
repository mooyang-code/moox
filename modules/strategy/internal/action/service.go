package action

import (
	"context"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
)

type Service struct {
	Repo   *store.Store
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
