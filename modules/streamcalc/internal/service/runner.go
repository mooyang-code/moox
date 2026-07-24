package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/streamcalc/internal/state"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/nats-io/nats.go"
)

type Runner struct {
	consumer   *events.Consumer
	process    *Processor
	batch      int
	checkpoint state.Store
	dlq        events.MessagePublisher
}

func (r *Runner) SetCheckpoint(store state.Store)                   { r.checkpoint = store }
func (r *Runner) SetDLQPublisher(publisher events.MessagePublisher) { r.dlq = publisher }

func (r *Runner) Restore(ctx context.Context) error {
	if r == nil || r.checkpoint == nil {
		return nil
	}
	snapshot, err := r.checkpoint.Load(ctx)
	if err != nil {
		return err
	}
	return r.process.Restore(snapshot)
}

func NewRunner(consumer *events.Consumer, process *Processor, batch int) (*Runner, error) {
	if consumer == nil || process == nil {
		return nil, fmt.Errorf("streamcalc runner dependencies are nil")
	}
	if batch <= 0 {
		batch = 1
	}
	return &Runner{consumer: consumer, process: process, batch: batch}, nil
}

func (r *Runner) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := r.RunOnce(ctx); err != nil && !errors.Is(err, nats.ErrTimeout) {
			return err
		}
	}
}

func (r *Runner) RunOnce(ctx context.Context) error {
	deliveries, fetchErr := r.consumer.Fetch(ctx, r.batch)
	var firstErr error
	if fetchErr != nil && !errors.Is(fetchErr, nats.ErrTimeout) && len(deliveries) == 0 {
		firstErr = fetchErr
	}
	for _, delivery := range deliveries {
		err := r.process.Process(ctx, delivery)
		switch {
		case err == nil:
			if r.checkpoint != nil {
				if saveErr := r.checkpoint.Save(ctx, r.process.Snapshot()); saveErr != nil && firstErr == nil {
					firstErr = fmt.Errorf("checkpoint streamcalc state: %w", saveErr)
					continue
				}
			}
			if ackErr := delivery.Delivery.Ack(ctx); ackErr != nil && firstErr == nil {
				firstErr = fmt.Errorf("ack streamcalc delivery: %w", ackErr)
			}
		case errors.Is(err, ErrLateData) || delivery.Err != nil:
			registry, dlqErr := events.DefaultRegistry()
			if dlqErr == nil {
				dlqErr = events.PublishRejected(ctx, r.dlq, registry, delivery.Delivery, err.Error(), "streamcalc")
			}
			if dlqErr != nil {
				if nakErr := delivery.Delivery.Nak(ctx, time.Second); nakErr != nil && firstErr == nil {
					firstErr = errors.Join(fmt.Errorf("publish streamcalc DLQ: %w", dlqErr), nakErr)
				} else if firstErr == nil {
					firstErr = fmt.Errorf("publish streamcalc DLQ: %w", dlqErr)
				}
				continue
			}
			if termErr := delivery.Delivery.Term(ctx); termErr != nil && firstErr == nil {
				firstErr = fmt.Errorf("term streamcalc delivery after DLQ: %w", termErr)
			}
		default:
			if nakErr := delivery.Delivery.Nak(ctx, time.Second); nakErr != nil && firstErr == nil {
				firstErr = fmt.Errorf("nak streamcalc delivery: %w", nakErr)
			}
		}
	}
	return firstErr
}
