package jetstream

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nats-io/nats.go"
	trpc "trpc.group/trpc-go/trpc-go"
)

type PublishAck struct {
	Stream    string
	Sequence  uint64
	Duplicate bool
}

// ProbePublishPermission is reserved for deployment tooling that verifies a
// credential cannot publish to an exact NATS API subject.
func (c *Client) ProbePublishPermission(ctx context.Context, subject, probeID string) error {
	_, err := c.PublishRaw(ctx, subject, probeID, []byte("{}"), "application/json")
	return err
}

// PublishRaw publishes a caller-owned protobuf envelope without making the
// transport package depend on a business event schema. packages/events uses
// this boundary for EventMessage; business modules must not call it directly.
func (c *Client) PublishRaw(ctx context.Context, subject, messageID string, payload []byte, contentType string) (*PublishAck, error) {
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConnection, err)
	}
	if err := c.alive(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConnection, err)
	}
	js, err := c.jetStream()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConnection, err)
	}
	if err := validateSubject(subject); err != nil {
		return nil, fmt.Errorf("%w: subject: %v", ErrInvalidMessage, err)
	}
	if strings.TrimSpace(messageID) == "" {
		return nil, fmt.Errorf("%w: message_id is required", ErrInvalidMessage)
	}
	if len(payload) == 0 {
		return nil, fmt.Errorf("%w: payload is required", ErrInvalidMessage)
	}
	maxPayload := c.maxPayload()
	if maxPayload > 0 && len(payload) > maxPayload {
		return nil, fmt.Errorf("%w: payload size %d exceeds %d", ErrInvalidMessage, len(payload), maxPayload)
	}
	contentType = strings.TrimSpace(contentType)
	if contentType == "" {
		return nil, fmt.Errorf("%w: content_type is required", ErrInvalidMessage)
	}
	natsMsg := &nats.Msg{Subject: subject, Header: nats.Header{}, Data: append([]byte(nil), payload...)}
	natsMsg.Header.Set(nats.MsgIdHdr, messageID)
	natsMsg.Header.Set("Content-Type", contentType)
	ack, err := js.PublishMsg(natsMsg, nats.Context(ctx))
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, fmt.Errorf("%w: %w", ErrConnection, ctx.Err())
		}
		if errors.Is(err, nats.ErrTimeout) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: %w", ErrPublishTimeout, err)
		}
		return nil, fmt.Errorf("%w: publish %s: %w", ErrConnection, subject, err)
	}
	return &PublishAck{Stream: ack.Stream, Sequence: ack.Sequence, Duplicate: ack.Duplicate}, nil
}
