package domain

import "time"

// OutboxMessage is a pending strategy event ready for publication.
type OutboxMessage struct {
	MessageID string
	Topic     string
	Payload   []byte
	CreatedAt time.Time
}

type OutboxStats struct {
	PendingCount  int64
	OldestPending time.Time
}
