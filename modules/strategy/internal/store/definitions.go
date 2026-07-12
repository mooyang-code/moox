package store

import (
	"context"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
)

func (s *Store) SaveDefinition(ctx context.Context, definition domain.StrategyDefinition) error {
	// Strategy versions are immutable. A database conflict is intentionally
	// surfaced to the registry service, which compares the existing hash.
	return s.db.WithContext(ctx).Create(&definition).Error
}

func (s *Store) GetDefinition(ctx context.Context, strategyID, version string) (domain.StrategyDefinition, error) {
	var definition domain.StrategyDefinition
	err := s.db.WithContext(ctx).Where("c_strategy_id=? AND c_version=?", strategyID, version).First(&definition).Error
	return definition, err
}

func (s *Store) EnableDefinition(ctx context.Context, strategyID, version string) error {
	return s.db.WithContext(ctx).Model(&domain.StrategyDefinition{}).
		Where("c_strategy_id=? AND c_version=?", strategyID, version).
		Update("c_status", "enabled").Error
}
