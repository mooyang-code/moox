package consumer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/messagepb"
	"github.com/nats-io/nats.go"
)

type PullConsumer interface {
	Fetch(context.Context, int) ([]*jetstream.Delivery, error)
	Close() error
}

type Runner struct {
	consumer PullConsumer
	handler  *Handler
	batch    int
}

func NewRunner(consumer PullConsumer, handler *Handler, batch int) *Runner {
	if batch <= 0 {
		batch = 1
	}
	return &Runner{consumer: consumer, handler: handler, batch: batch}
}

func (r *Runner) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		deliveries, err := r.consumer.Fetch(ctx, r.batch)
		if err != nil && len(deliveries) == 0 {
			if errors.Is(err, nats.ErrTimeout) {
				continue
			}
			return fmt.Errorf("fetch archive deliveries: %w", err)
		}
		retryBatch := false
		for i, delivery := range deliveries {
			if handleErr := r.handler.Handle(ctx, deliveryAdapter{delivery}); handleErr != nil {
				var retry *RetryScheduledError
				if errors.As(handleErr, &retry) {
					for _, remaining := range deliveries[i+1:] {
						_ = remaining.Nak(ctx, retry.Delay)
					}
					if err := sleepContext(ctx, retry.Delay); err != nil {
						return err
					}
					retryBatch = true
					break
				}
				return handleErr
			}
		}
		if retryBatch {
			continue
		}
		if err != nil && !errors.Is(err, jetstream.ErrDecode) {
			return fmt.Errorf("fetch archive deliveries: %w", err)
		}
	}
	return ctx.Err()
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type deliveryAdapter struct{ *jetstream.Delivery }

func (d deliveryAdapter) Envelope() *messagepb.MooxMessage { return d.Message }
func (d deliveryAdapter) RawEnvelope() []byte              { return d.RawData }
func (d deliveryAdapter) MessageID() string {
	if d.Message != nil {
		return d.Message.GetMessageId()
	}
	return d.RawMessageID
}
func (d deliveryAdapter) Subject() string        { return d.Delivery.Subject }
func (d deliveryAdapter) StreamSequence() uint64 { return d.Delivery.StreamSeq }
func (d deliveryAdapter) DeliveryCount() uint64  { return d.Delivery.DeliveryCount }
func (d deliveryAdapter) DecodeError() error     { return d.Delivery.DecodeError }
