package store

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
)

func (s *Store) ListPendingOutbox(ctx context.Context, limit int, now time.Time) ([]domain.OutboxMessage, error) {
	if limit <= 0 {
		return []domain.OutboxMessage{}, nil
	}
	var rows []domain.OutboxMessage
	err := s.db.WithContext(ctx).Table("t_strategy_outbox").
		Select("c_message_id AS message_id, c_topic AS topic, c_payload AS payload, c_ctime AS created_at").
		Where("c_published=0 AND (c_claimed_until IS NULL OR c_claimed_until < ?)", now).
		Order("c_ctime").Limit(limit).Find(&rows).Error
	return rows, err
}

func (s *Store) PendingOutboxStats(ctx context.Context) (domain.OutboxStats, error) {
	query := s.db.WithContext(ctx).Table("t_strategy_outbox").Where("c_published=0")
	var stats domain.OutboxStats
	if err := query.Count(&stats.PendingCount).Error; err != nil {
		return domain.OutboxStats{}, err
	}
	if stats.PendingCount == 0 {
		return stats, nil
	}
	var oldest domain.OutboxMessage
	if err := query.Select("c_ctime AS created_at").Order("c_ctime").Limit(1).Scan(&oldest).Error; err != nil {
		return domain.OutboxStats{}, err
	}
	stats.OldestPending = oldest.CreatedAt.UTC()
	return stats, nil
}

func (s *Store) ClaimOutbox(ctx context.Context, messageID, token string, now time.Time, lease time.Duration) (bool, error) {
	result := s.db.WithContext(ctx).Table("t_strategy_outbox").
		Where("c_message_id=? AND c_published=0 AND (c_claimed_until IS NULL OR c_claimed_until < ?)", messageID, now).
		Updates(map[string]any{"c_claimed_until": now.Add(lease), "c_claim_token": token})
	return result.RowsAffected == 1, result.Error
}

func (s *Store) ReleaseOutbox(ctx context.Context, messageID, token string) error {
	result := s.db.WithContext(ctx).Table("t_strategy_outbox").
		Where("c_message_id=? AND c_claim_token=?", messageID, token).
		Updates(map[string]any{"c_claimed_until": nil, "c_claim_token": ""})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("release strategy outbox %s: claim lost", messageID)
	}
	return nil
}

func (s *Store) MarkOutboxPublished(ctx context.Context, messageID, token string) error {
	result := s.db.WithContext(ctx).Table("t_strategy_outbox").
		Where("c_message_id=? AND c_claim_token=?", messageID, token).
		Updates(map[string]any{"c_published": 1, "c_claimed_until": nil, "c_claim_token": ""})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("mark strategy outbox %s published: claim lost", messageID)
	}
	return nil
}
