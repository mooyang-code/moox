package store

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
)

func (s *Store) ListPendingOutbox(ctx context.Context, limit int, now time.Time) ([]domain.OutboxMessage, error) {
	if limit <= 0 {
		return []domain.OutboxMessage{}, nil
	}
	var rows []domain.OutboxMessage
	err := s.db.WithContext(ctx).Table("t_strategy_outbox").
		Select("c_message_id AS message_id, c_topic AS topic, c_payload AS payload").
		Where("c_published=0 AND (c_claimed_until IS NULL OR c_claimed_until < ?)", now).
		Order("c_ctime").Limit(limit).Find(&rows).Error
	return rows, err
}

func (s *Store) ClaimOutbox(ctx context.Context, messageID, token string, now time.Time, lease time.Duration) (bool, error) {
	result := s.db.WithContext(ctx).Table("t_strategy_outbox").
		Where("c_message_id=? AND c_published=0 AND (c_claimed_until IS NULL OR c_claimed_until < ?)", messageID, now).
		Updates(map[string]any{"c_claimed_until": now.Add(lease), "c_claim_token": token})
	return result.RowsAffected == 1, result.Error
}

func (s *Store) ReleaseOutbox(ctx context.Context, messageID, token string) error {
	return s.db.WithContext(ctx).Table("t_strategy_outbox").
		Where("c_message_id=? AND c_claim_token=?", messageID, token).
		Updates(map[string]any{"c_claimed_until": nil, "c_claim_token": ""}).Error
}

func (s *Store) MarkOutboxPublished(ctx context.Context, messageID, token string) error {
	return s.db.WithContext(ctx).Table("t_strategy_outbox").
		Where("c_message_id=? AND c_claim_token=?", messageID, token).
		Updates(map[string]any{"c_published": 1, "c_claimed_until": nil, "c_claim_token": ""}).Error
}
