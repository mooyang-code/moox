package store

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
)

func (s *Store) PendingOutboxStats(ctx context.Context) (domain.OutboxStats, error) {
	query := s.db.WithContext(ctx).Table("t_strategy_results").Where("publish_status = ? AND event_data IS NOT NULL", PublishPending)
	var stats domain.OutboxStats
	if err := query.Count(&stats.PendingCount).Error; err != nil {
		return domain.OutboxStats{}, err
	}
	if stats.PendingCount == 0 {
		return stats, nil
	}
	var oldest int64
	if err := query.Select("created_at").Order("created_at, result_id").Limit(1).Scan(&oldest).Error; err != nil {
		return domain.OutboxStats{}, err
	}
	stats.OldestPending = time.UnixMilli(oldest).UTC()
	return stats, nil
}
