package store

import (
	"context"
	"errors"
	"time"
)

func (s *Store) IsProcessed(ctx context.Context, messageID string) (bool, error) {
	if messageID == "" {
		return false, errors.New("strategy inbox message_id is required")
	}
	_, ok := s.processed.Load(messageID)
	return ok, nil
}

func (s *Store) MarkProcessed(ctx context.Context, messageID, eventName string, receivedAt time.Time) error {
	if messageID == "" || eventName == "" || receivedAt.IsZero() {
		return errors.New("strategy inbox identity is incomplete")
	}
	s.processed.Store(messageID, struct{}{})
	return nil
}
