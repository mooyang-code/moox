package jetstream

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/packages/messagepb"
	"github.com/nats-io/nats.go"
)

type Delivery struct {
	Message         *messagepb.MooxMessage
	Subject         string
	Stream          string
	Consumer        string
	StreamSeq       uint64
	ConsumerSeq     uint64
	DeliveryCount   uint64
	PersistentToken string
	// DecodeError and RawData are populated when a consumer opts into poison
	// delivery. Message is nil in that case and the caller must Term or NAK it.
	DecodeError  error
	RawData      []byte
	RawMessageID string

	msg    *nats.Msg
	client *Client
}

func (d *Delivery) Ack(ctx context.Context) error {
	if d != nil && d.msg == nil && d.client != nil {
		return d.client.AckToken(ctx, d.PersistentToken)
	}
	return d.withMessage(ctx, func(msg *nats.Msg, ctx context.Context) error { return msg.AckSync(nats.Context(ctx)) })
}

func (d *Delivery) Nak(ctx context.Context, delay time.Duration) error {
	if d != nil && d.msg == nil && d.client != nil {
		return d.client.NakToken(ctx, d.PersistentToken, delay)
	}
	return d.withMessage(ctx, func(msg *nats.Msg, ctx context.Context) error {
		if delay > 0 {
			return msg.NakWithDelay(delay, nats.Context(ctx))
		}
		return msg.Nak(nats.Context(ctx))
	})
}

func (d *Delivery) InProgress(ctx context.Context) error {
	if d != nil && d.msg == nil && d.client != nil {
		return d.client.InProgressToken(ctx, d.PersistentToken)
	}
	return d.withMessage(ctx, func(msg *nats.Msg, ctx context.Context) error { return msg.InProgress(nats.Context(ctx)) })
}

func (d *Delivery) Term(ctx context.Context) error {
	if d != nil && d.msg == nil && d.client != nil {
		return d.client.TermToken(ctx, d.PersistentToken)
	}
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
	if msg == nil {
		return nil, fmt.Errorf("%w: NATS message is nil", ErrDecode)
	}
	delivery := &Delivery{
		Subject:      msg.Subject,
		Stream:       stream,
		Consumer:     consumer,
		RawData:      append([]byte(nil), msg.Data...),
		RawMessageID: msg.Header.Get(nats.MsgIdHdr),
		msg:          msg,
	}
	token, tokenErr := encodeDeliveryToken(stream, consumer, msg.Reply)
	if tokenErr == nil {
		delivery.PersistentToken = token
	}
	decoded, err := decodeNATSMessage(msg, maxPayload)
	if err != nil {
		delivery.DecodeError = err
		return delivery, err
	}
	metadata, err := msg.Metadata()
	if err != nil {
		delivery.DecodeError = fmt.Errorf("%w: metadata: %w", ErrDecode, err)
		return delivery, delivery.DecodeError
	}
	delivery.Message = decoded
	delivery.StreamSeq = metadata.Sequence.Stream
	delivery.ConsumerSeq = metadata.Sequence.Consumer
	delivery.DeliveryCount = metadata.NumDelivered
	return delivery, nil
}
