package store

import (
	"context"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
)

func (s *Store) SaveStrategy(ctx context.Context, strategy domain.Strategy) error {
	return s.db.WithContext(ctx).Exec(`
		INSERT INTO t_strategies (
			strategy_id, name, manifest_yaml, source_code, source_hash, created_at
		) VALUES (?, ?, ?, ?, ?, ?)
	`, strategy.ID, strategy.Name, strategy.ManifestYAML, strategy.SourceCode, strategy.SourceHash, strategy.CreatedAt.UTC().UnixMilli()).Error
}

func (s *Store) GetStrategy(ctx context.Context, strategyID string) (domain.Strategy, error) {
	var row strategyRow
	err := s.db.WithContext(ctx).Table("t_strategies").
		Where("strategy_id = ?", strategyID).
		Take(&row).Error
	return row.domain(), err
}
