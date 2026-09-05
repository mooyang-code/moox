package store

import (
	"context"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
)

func (s *Store) SaveStrategy(ctx context.Context, strategy domain.Strategy) error {
	updatedAt := strategy.CreatedAt
	err := s.db.WithContext(ctx).Exec(`
		INSERT INTO t_strategies (
			strategy_id, strategy_name, dsl_yaml, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?)
	`, strategy.ID, strategy.Name, strategy.ManifestYAML, strategy.CreatedAt.UTC().UnixMilli(), updatedAt.UTC().UnixMilli()).Error
	if err == nil && len(strategy.CompiledJSON) > 0 {
		s.legacyCompiled.Store(strategy.ID, append([]byte(nil), strategy.CompiledJSON...))
	}
	return err
}

func (s *Store) GetStrategy(ctx context.Context, strategyID string) (domain.Strategy, error) {
	var row strategyRow
	err := s.db.WithContext(ctx).Table("t_strategies").
		Where("strategy_id = ?", strategyID).
		Take(&row).Error
	value := row.domain()
	if compiled, ok := s.legacyCompiled.Load(strategyID); ok {
		value.CompiledJSON = append([]byte(nil), compiled.([]byte)...)
		value.Kind = "coin_selection"
	}
	return value, err
}

func (s *Store) ListStrategies(ctx context.Context) ([]domain.Strategy, error) {
	var rows []strategyRow
	if err := s.db.WithContext(ctx).Table("t_strategies").
		Order("strategy_name, strategy_id").Find(&rows).Error; err != nil {
		return nil, err
	}
	strategies := make([]domain.Strategy, 0, len(rows))
	for _, row := range rows {
		strategies = append(strategies, row.domain())
	}
	return strategies, nil
}
