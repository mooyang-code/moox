package domain

// OutboxMessage is a pending strategy event ready for publication.
type OutboxMessage struct {
	MessageID string
	Topic     string
	Payload   []byte
}
