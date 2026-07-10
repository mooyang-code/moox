package jetstream

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/packages/messagepb"
	"github.com/nats-io/nats.go"
)

type Delivery struct {
	Message       *messagepb.MooxMessage
	Subject       string
	Stream        string
	Consumer      string
	StreamSeq     uint64
	ConsumerSeq   uint64
	DeliveryCount uint64

	msg *nats.Msg
}

func (d *Delivery) Ack(ctx context.Context) error {
	return d.withMessage(ctx, func(msg *nats.Msg, ctx context.Context) error { return msg.AckSync(nats.Context(ctx)) })
}

func (d *Delivery) Nak(ctx context.Context, delay time.Duration) error {
	return d.withMessage(ctx, func(msg *nats.Msg, ctx context.Context) error {
		if delay > 0 {
			return msg.NakWithDelay(delay, nats.Context(ctx))
		}
		return msg.Nak(nats.Context(ctx))
	})
}

func (d *Delivery) InProgress(ctx context.Context) error {
	return d.withMessage(ctx, func(msg *nats.Msg, ctx context.Context) error { return msg.InProgress(nats.Context(ctx)) })
}

func (d *Delivery) Term(ctx context.Context) error {
	return d.withMessage(ctx, func(msg *nats.Msg, ctx context.Context) error { return msg.Term(nats.Context(ctx)) })
}

func (d *Delivery) withMessage(ctx context.Context, fn func(*nats.Msg, context.Context) error) error {
	if d == nil || d.msg == nil {
		return ErrInvalidDelivery
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidDelivery, err)
	}
	if err := fn(d.msg, ctx); err != nil {
		return err
	}
	return nil
}

func deliveryFromMessage(msg *nats.Msg, stream, consumer string, maxPayload int) (*Delivery, error) {
	decoded, err := decodeNATSMessage(msg, maxPayload)
	if err != nil {
		return nil, err
	}
	metadata, err := msg.Metadata()
	if err != nil {
		return nil, fmt.Errorf("%w: metadata: %w", ErrDecode, err)
	}
	return &Delivery{
		Message:       decoded,
		Subject:       msg.Subject,
		Stream:        stream,
		Consumer:      consumer,
		StreamSeq:     metadata.Sequence.Stream,
		ConsumerSeq:   metadata.Sequence.Consumer,
		DeliveryCount: metadata.NumDelivered,
		msg:           msg,
	}, nil
}
