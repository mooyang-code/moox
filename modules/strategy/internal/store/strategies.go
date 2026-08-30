package store

import (
	"context"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
)

func (s *Store) SaveStrategy(ctx context.Context, strategy domain.Strategy) error {
	return s.db.WithContext(ctx).Exec(`
		INSERT INTO t_strategies (
			strategy_id, name, kind, manifest_yaml, compiled_json, source_hash, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`, strategy.ID, strategy.Name, strategy.Kind, strategy.ManifestYAML, string(strategy.CompiledJSON), strategy.SourceHash, strategy.CreatedAt.UTC().UnixMilli()).Error
}

func (s *Store) GetStrategy(ctx context.Context, strategyID string) (domain.Strategy, error) {
	var row strategyRow
	err := s.db.WithContext(ctx).Table("t_strategies").
		Where("strategy_id = ?", strategyID).
		Take(&row).Error
	return row.domain(), err
}

func (s *Store) ListStrategies(ctx context.Context) ([]domain.Strategy, error) {
	var rows []strategyRow
	if err := s.db.WithContext(ctx).Table("t_strategies").
		Order("created_at DESC, strategy_id").Find(&rows).Error; err != nil {
		return nil, err
	}
	strategies := make([]domain.Strategy, 0, len(rows))
	for _, row := range rows {
		strategies = append(strategies, row.domain())
	}
	return strategies, nil
}
