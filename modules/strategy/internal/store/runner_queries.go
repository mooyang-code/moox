package store

import (
	"context"
	"encoding/json"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
)

type RunnerFilter struct {
	StrategyID string
	SpaceID    string
	Status     domain.RunnerStatus
}

func (s *Store) ListRunners(ctx context.Context, filter RunnerFilter) ([]domain.StrategyRunner, error) {
	query := s.db.WithContext(ctx).Table("t_strategy_instances")
	if filter.StrategyID != "" {
		query = query.Where("strategy_id = ?", filter.StrategyID)
	}
	if filter.SpaceID != "" {
		query = query.Where("space_id = ?", filter.SpaceID)
	}
	if filter.Status != "" {
		enabled := filter.Status == domain.RunnerStatusEnabled
		query = query.Where("enabled = ?", boolInt(enabled))
	}
	var rows []instanceRow
	if err := query.Order("instance_id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	runners := make([]domain.StrategyRunner, 0, len(rows))
	for _, row := range rows {
		instance := row.instance()
		var binding struct {
			SourceViewID string `json:"source_view_id"`
			Frequency    string `json:"frequency"`
		}
		_ = json.Unmarshal(instance.InputBindingsJSON, &binding)
		status := domain.RunnerStatusDisabled
		if instance.Enabled {
			status = domain.RunnerStatusEnabled
		}
		runners = append(runners, domain.StrategyRunner{
			ID: instance.InstanceID, StrategyID: instance.StrategyID, SpaceID: instance.SpaceID,
			SourceViewID: binding.SourceViewID, Frequency: binding.Frequency,
			LogicalAccountID: instance.LogicalAccountID, Status: status,
			CurrentTargetsJSON: json.RawMessage(`[]`), CreatedAt: instance.CreatedAt, UpdatedAt: instance.UpdatedAt,
		})
	}
	return runners, nil
}
