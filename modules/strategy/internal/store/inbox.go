package store

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

func (s *Store) IsProcessed(ctx context.Context, messageID string) (bool, error) {
	if messageID == "" {
		return false, errors.New("strategy inbox message_id is required")
	}
	var count int64
	err := s.db.WithContext(ctx).Table("t_strategy_inbox").Where("message_id = ?", messageID).Count(&count).Error
	return count > 0, err
}

func (s *Store) MarkProcessed(ctx context.Context, messageID, eventName string, receivedAt time.Time) error {
	if messageID == "" || eventName == "" || receivedAt.IsZero() {
		return errors.New("strategy inbox identity is incomplete")
	}
	err := s.db.WithContext(ctx).Exec(`
		INSERT INTO t_strategy_inbox (message_id, event_name, received_at)
		VALUES (?, ?, ?)
		ON CONFLICT(message_id) DO NOTHING
	`, messageID, eventName, receivedAt.UTC().UnixMilli()).Error
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return nil
	}
	return err
}
