package store

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
)

func (s *Store) ListPendingOutbox(ctx context.Context, limit int) ([]domain.OutboxMessage, error) {
	if limit <= 0 {
		return []domain.OutboxMessage{}, nil
	}
	var rows []struct {
		MessageID string `gorm:"column:result_id"`
		EventData []byte `gorm:"column:event_data"`
		CreatedAt int64  `gorm:"column:created_at"`
	}
	err := s.db.WithContext(ctx).Table("t_strategy_results").
		Where("publish_status = ? AND event_data IS NOT NULL", PublishPending).
		Order("created_at, result_id").Limit(limit).Find(&rows).Error
	if err != nil {
		// Compatibility for old embedded stores; the current schema never
		// creates this table.
		var legacy []struct {
			MessageID string `gorm:"column:message_id"`
			EventData []byte `gorm:"column:event_data"`
			CreatedAt int64  `gorm:"column:created_at"`
		}
		if legacyErr := s.db.WithContext(ctx).Table("t_strategy_outbox").Order("created_at, message_id").Limit(limit).Find(&legacy).Error; legacyErr != nil {
			return nil, err
		}
		messages := make([]domain.OutboxMessage, 0, len(legacy))
		for _, row := range legacy {
			messages = append(messages, domain.OutboxMessage{MessageID: row.MessageID, EventData: row.EventData, CreatedAt: time.UnixMilli(row.CreatedAt).UTC()})
		}
		return messages, nil
	}
	messages := make([]domain.OutboxMessage, 0, len(rows))
	for _, row := range rows {
		messages = append(messages, domain.OutboxMessage{
			MessageID: row.MessageID,
			EventData: row.EventData,
			CreatedAt: time.UnixMilli(row.CreatedAt).UTC(),
		})
	}
	return messages, nil
}

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

func (s *Store) DeleteOutbox(ctx context.Context, messageID string) error {
	result := s.db.WithContext(ctx).Exec(
		"UPDATE t_strategy_results SET publish_status = 'cancelled' WHERE result_id = ? AND publish_status = 'pending'",
		messageID,
	)
	if result.Error != nil {
		legacy := s.db.WithContext(ctx).Exec("DELETE FROM t_strategy_outbox WHERE message_id = ?", messageID)
		if legacy.Error != nil {
			return result.Error
		}
		if legacy.RowsAffected == 0 {
			return fmt.Errorf("cancel strategy result %s: row missing", messageID)
		}
		return nil
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("cancel strategy result %s: row missing", messageID)
	}
	return nil
}
