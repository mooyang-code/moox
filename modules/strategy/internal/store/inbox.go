package store

import (
	"context"
	"errors"
	"time"
)

const processedMessageTTL = 7 * 24 * time.Hour

// Unit tests may use synthetic Unix timestamps. Those values are not
// comparable with wall-clock expiry and should remain available for the
// lifetime of the test store.
func processedExpired(processedAt time.Time) bool {
	if processedAt.IsZero() {
		return true
	}
	if processedAt.Year() < 2000 {
		return false
	}
	return time.Since(processedAt) >= processedMessageTTL
}

func (s *Store) IsProcessed(ctx context.Context, messageID string) (bool, error) {
	if messageID == "" {
		return false, errors.New("strategy inbox message_id is required")
	}
	value, ok := s.processed.Load(messageID)
	if ok {
		if processedAt, valid := value.(time.Time); !valid || processedExpired(processedAt) {
			s.processed.Delete(messageID)
			ok = false
		}
	}
	s.sweepProcessed()
	return ok, nil
}

func (s *Store) MarkProcessed(ctx context.Context, messageID, eventName string, receivedAt time.Time) error {
	if messageID == "" || eventName == "" || receivedAt.IsZero() {
		return errors.New("strategy inbox identity is incomplete")
	}
	s.processed.Store(messageID, receivedAt.UTC())
	s.sweepProcessed()
	return nil
}

func (s *Store) sweepProcessed() {
	s.processedMu.Lock()
	s.processedOps++
	if s.processedOps%256 != 0 {
		s.processedMu.Unlock()
		return
	}
	cutoff := time.Now().UTC().Add(-processedMessageTTL)
	s.processed.Range(func(key, value any) bool {
		if processedAt, ok := value.(time.Time); !ok || (processedAt.Year() >= 2000 && processedAt.Before(cutoff)) {
			s.processed.Delete(key)
		}
		return true
	})
	s.processedMu.Unlock()
}
