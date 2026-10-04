package jetstream

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	trpc "trpc.group/trpc-go/trpc-go"
)

type Delivery struct {
	Subject       string
	Stream        string
	Consumer      string
	StreamSeq     uint64
	ConsumerSeq   uint64
	DeliveryCount uint64
	// DecodeError and RawData are populated when a consumer opts into transport
	// validation errors. Business EventMessage decoding happens in packages/events.
	DecodeError  error
	RawData      []byte
	RawMessageID string
	ContentType  string

	msg        *nats.Msg
	ackFn      func(context.Context) error
	nakFn      func(context.Context, time.Duration) error
	termFn     func(context.Context) error
	progressFn func(context.Context) error

	actionTimeout time.Duration
}

const defaultDeliveryActionTimeout = 5 * time.Second

func (d *Delivery) Ack(ctx context.Context) error {
	return d.withActionTimeout(ctx, func(actionCtx context.Context) error {
		if d != nil && d.ackFn != nil {
			return d.ackFn(actionCtx)
		}
		return d.withMessage(actionCtx, func(msg *nats.Msg, _ context.Context) error { return msg.Ack() })
	})
}

func (d *Delivery) Nak(ctx context.Context, delay time.Duration) error {
	return d.withActionTimeout(ctx, func(actionCtx context.Context) error {
		if d != nil && d.nakFn != nil {
			return d.nakFn(actionCtx, delay)
		}
		return d.withMessage(actionCtx, func(msg *nats.Msg, ctx context.Context) error {
			if delay > 0 {
				return msg.NakWithDelay(delay, nats.Context(ctx))
			}
			return msg.Nak(nats.Context(ctx))
		})
	})
}

func (d *Delivery) InProgress(ctx context.Context) error {
	return d.withActionTimeout(ctx, func(actionCtx context.Context) error {
		if d != nil && d.progressFn != nil {
			return d.progressFn(actionCtx)
		}
		return d.withMessage(actionCtx, func(msg *nats.Msg, ctx context.Context) error { return msg.InProgress(nats.Context(ctx)) })
	})
}

func (d *Delivery) Term(ctx context.Context) error {
	return d.withActionTimeout(ctx, func(actionCtx context.Context) error {
		if d != nil && d.termFn != nil {
			return d.termFn(actionCtx)
		}
		return d.withMessage(actionCtx, func(msg *nats.Msg, ctx context.Context) error { return msg.Term(nats.Context(ctx)) })
	})
}

func (d *Delivery) withActionTimeout(ctx context.Context, action func(context.Context) error) error {
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	timeout := defaultDeliveryActionTimeout
	if d != nil && d.actionTimeout > 0 {
		timeout = d.actionTimeout
	}
	actionCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return action(actionCtx)
}

func (d *Delivery) withMessage(ctx context.Context, fn func(*nats.Msg, context.Context) error) error {
	if d == nil || d.msg == nil {
		return ErrInvalidDelivery
	}
	if ctx == nil {
		ctx = trpc.BackgroundContext()
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
	return rawDeliveryFromMessage(msg, stream, consumer, maxPayload)
}

func rawDeliveryFromMessage(msg *nats.Msg, stream, consumer string, maxPayload int) (*Delivery, error) {
	if msg == nil {
		return nil, fmt.Errorf("%w: NATS message is nil", ErrDecode)
	}
	delivery := &Delivery{
		Subject:      msg.Subject,
		Stream:       stream,
		Consumer:     consumer,
		RawData:      append([]byte(nil), msg.Data...),
		RawMessageID: msg.Header.Get(nats.MsgIdHdr),
		ContentType:  msg.Header.Get("Content-Type"),
		msg:          msg,
	}
	if delivery.RawMessageID == "" {
		delivery.DecodeError = fmt.Errorf("%w: message_id header is required", ErrDecode)
		return delivery, delivery.DecodeError
	}
	if maxPayload > 0 && len(msg.Data) > maxPayload {
		delivery.DecodeError = fmt.Errorf("%w: raw payload size %d exceeds %d", ErrDecode, len(msg.Data), maxPayload)
		return delivery, delivery.DecodeError
	}
	metadata, err := msg.Metadata()
	if err != nil {
		delivery.DecodeError = fmt.Errorf("%w: metadata: %w", ErrDecode, err)
		return delivery, delivery.DecodeError
	}
	delivery.StreamSeq = metadata.Sequence.Stream
	delivery.ConsumerSeq = metadata.Sequence.Consumer
	delivery.DeliveryCount = metadata.NumDelivered
	return delivery, nil
}
