package jetstream

import "errors"

var (
	// ErrInvalidMessage means a message does not satisfy the MooX wire contract.
	ErrInvalidMessage = errors.New("invalid moox message")
	// ErrConnection means the client is not connected to NATS or a NATS operation failed.
	ErrConnection = errors.New("jetstream connection error")
	// ErrPublishTimeout means JetStream did not return a publication acknowledgement in time.
	ErrPublishTimeout = errors.New("jetstream publish timeout")
	// ErrDecode means a received NATS message is not a valid MooX message.
	ErrDecode = errors.New("decode moox message")
	// ErrInvalidConsumer means a pull consumer configuration is invalid.
	ErrInvalidConsumer  = errors.New("invalid pull consumer")
	ErrConsumerNotFound = errors.New("pull consumer not found")
	// ErrInvalidDelivery means a delivery is nil or no longer usable.
	ErrInvalidDelivery = errors.New("invalid delivery")
	// ErrClosed means the client or consumer has already been closed.
	ErrClosed = errors.New("jetstream client is closed")
)
