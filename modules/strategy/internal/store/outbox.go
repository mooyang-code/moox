package store

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
)

func (s *Store) ListPendingOutbox(ctx context.Context, limit int) ([]domain.OutboxMessage, error) {
	if limit <= 0 {
		return []domain.OutboxMessage{}, nil
	}
	var rows []domain.OutboxMessage
	err := s.db.WithContext(ctx).Table("t_strategy_outbox").
		Select("c_message_id AS message_id, c_event_data AS event_data, c_ctime AS created_at").
		Order("c_id ASC").Limit(limit).Find(&rows).Error
	return rows, err
}

func (s *Store) PendingOutboxStats(ctx context.Context) (domain.OutboxStats, error) {
	query := s.db.WithContext(ctx).Table("t_strategy_outbox")
	var stats domain.OutboxStats
	if err := query.Count(&stats.PendingCount).Error; err != nil {
		return domain.OutboxStats{}, err
	}
	if stats.PendingCount == 0 {
		return stats, nil
	}
	var oldest domain.OutboxMessage
	if err := query.Select("c_ctime AS created_at").Order("c_id ASC").Limit(1).Scan(&oldest).Error; err != nil {
		return domain.OutboxStats{}, err
	}
	stats.OldestPending = oldest.CreatedAt.UTC()
	return stats, nil
}

func (s *Store) DeleteOutbox(ctx context.Context, messageID string) error {
	result := s.db.WithContext(ctx).Table("t_strategy_outbox").
		Where("c_message_id=?", messageID).Delete(nil)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("delete strategy outbox %s: row missing", messageID)
	}
	return nil
}
