package store

import (
	"context"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
)

type RunnerFilter struct {
	StrategyID string
	SpaceID    string
	Status     domain.RunnerStatus
}

func (s *Store) ListRunners(ctx context.Context, filter RunnerFilter) ([]domain.StrategyRunner, error) {
	query := s.db.WithContext(ctx).Table("t_strategy_runners")
	if filter.StrategyID != "" {
		query = query.Where("strategy_id = ?", filter.StrategyID)
	}
	if filter.SpaceID != "" {
		query = query.Where("space_id = ?", filter.SpaceID)
	}
	if filter.Status != "" {
		query = query.Where("status = ?", filter.Status)
	}
	var rows []runnerRow
	if err := query.Order("runner_id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	runners := make([]domain.StrategyRunner, 0, len(rows))
	for _, row := range rows {
		runners = append(runners, row.domain())
	}
	return runners, nil
}
