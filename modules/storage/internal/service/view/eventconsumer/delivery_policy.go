package eventconsumer

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/storagepb"
)

func (c *Consumer) processDeliveryWithPolicy(ctx context.Context, delivery *jetstream.Delivery, heartbeat *deliveryHeartbeat, maxRetryAttempts int) error {
	return c.processDeliveryWithApply(ctx, delivery, heartbeat, maxRetryAttempts, func(ctx context.Context, delivery *jetstream.Delivery) error {
		return c.applyDelivery(ctx, delivery)
	})
}

func (c *Consumer) processDeliveryWithApply(ctx context.Context, delivery *jetstream.Delivery, heartbeat *deliveryHeartbeat, maxRetryAttempts int, apply func(context.Context, *jetstream.Delivery) error) error {
	return c.processDeliveryWithApplyAndActions(ctx, delivery, heartbeat, maxRetryAttempts, apply, deliveryActions{
		ack:      delivery.Ack,
		progress: delivery.InProgress,
		term:     delivery.Term,
	})
}

type deliveryActions struct {
	ack      func(context.Context) error
	progress func(context.Context) error
	term     func(context.Context) error
}

func (c *Consumer) processDeliveryWithApplyAndActions(ctx context.Context, delivery *jetstream.Delivery, heartbeat *deliveryHeartbeat, maxRetryAttempts int, apply func(context.Context, *jetstream.Delivery) error, actions deliveryActions) error {
	if c == nil {
		return errors.New("storage view event consumer is nil")
	}
	if delivery == nil {
		return errors.New("storage view delivery is nil")
	}
	if apply == nil || actions.ack == nil || actions.progress == nil || actions.term == nil {
		return errors.New("storage view delivery policy is incomplete")
	}
	if ctx == nil {
		return errors.New("storage view delivery context is required")
	}
	started := time.Now()
	metrics := c.config.Metrics
	if metrics == nil {
		metrics = observability.DefaultViewMetrics
	}
	defer func() { metrics.ObserveDeliveryDuration(time.Since(started)) }()
	if delivery != nil && delivery.DeliveryCount > 1 {
		metrics.IncRedelivery()
	}
	if maxRetryAttempts < 1 {
		maxRetryAttempts = defaultMaxRetryAttempts
	}
	if heartbeat == nil {
		heartbeat = newDeliveryHeartbeat(ctx, delivery, deliveryHeartbeatInterval(120*time.Second), metrics)
	}
	defer func() { heartbeat.stop() }()
	retryCount := 0
	for ctx.Err() == nil {
		if c.config.Lease != nil {
			if err := c.config.Lease.Acquire(ctx); err != nil {
				return errors.Join(err, heartbeat.err())
			}
		}
		err := apply(ctx, delivery)
		if c.config.Lease != nil {
			c.config.Lease.Release()
		}
		if err == nil {
			// Applying an event is deliberately separate from ACK retry:
			// an ACK transport failure must never repeat an already successful
			// index write.
			for ctx.Err() == nil {
				if ackErr := actions.ack(ctx); ackErr == nil {
					metrics.ObserveDelivery("ack", "success")
					return heartbeat.err()
				} else {
					log.Printf("storage view delivery ack failed: %v", ackErr)
					metrics.IncAckError()
					metrics.ObserveDelivery("ack", "error")
					heartbeat.report(ackErr)
				}
				if !sleepDeliveryRetry(ctx, time.Second) {
					return ctx.Err()
				}
			}
			return ctx.Err()
		}
		if IsPermanent(err) {
			// A malformed row/marker cannot be skipped: MaxAckPending=1 is the
			// rows-before-marker ordering fence.  The default unlimited policy
			// therefore keeps the poison delivery pending for operator repair;
			// only an explicit positive emergency limit permits TERM.
			if maxRetryAttempts <= 0 {
				if progressErr := actions.progress(ctx); progressErr != nil {
					metrics.IncInProgressError()
					metrics.ObserveDelivery("in_progress", "error")
					heartbeat.report(progressErr)
				} else {
					metrics.ObserveDelivery("in_progress", "success")
				}
				if !sleepDeliveryRetry(ctx, time.Second) {
					return ctx.Err()
				}
				continue
			}
			for ctx.Err() == nil {
				if termErr := actions.term(ctx); termErr == nil {
					metrics.ObserveDelivery("term", "success")
					return heartbeat.err()
				} else {
					log.Printf("storage view delivery term failed after permanent error %v: %v", err, termErr)
					metrics.IncAckError()
					metrics.ObserveDelivery("term", "error")
					heartbeat.report(termErr)
					if errors.Is(termErr, jetstream.ErrInvalidDelivery) || errors.Is(termErr, jetstream.ErrClosed) {
						return errors.Join(err, termErr, heartbeat.err())
					}
				}
				if !sleepDeliveryRetry(ctx, time.Second) {
					return ctx.Err()
				}
			}
			return ctx.Err()
		}
		retryCount++
		if maxRetryAttempts > 0 && retryCount >= maxRetryAttempts {
			metrics.IncRetryExhausted()
			log.Printf("storage view delivery retry exhausted: consumer=%s event_id=%s subject=%s delivery_count=%d decision=TERM reason=%v",
				delivery.Consumer, delivery.RawMessageID, delivery.Subject, delivery.DeliveryCount, err)
			if termErr := actions.term(ctx); termErr != nil {
				metrics.IncAckError()
				metrics.ObserveDelivery("term", "error")
				return errors.Join(err, termErr, heartbeat.err())
			}
			metrics.ObserveDelivery("term", "success")
			return errors.Join(err, heartbeat.err())
		}
		// Keep the delivery pending while retrying. NAK would release
		// MaxAckPending and allow a later event to overtake it.
		if progressErr := actions.progress(ctx); progressErr != nil {
			log.Printf("storage view delivery progress failed after %v: %v", err, progressErr)
			metrics.IncInProgressError()
			metrics.ObserveDelivery("in_progress", "error")
			heartbeat.report(progressErr)
		} else {
			metrics.ObserveDelivery("in_progress", "success")
		}
		if !sleepDeliveryRetry(ctx, time.Second) {
			return ctx.Err()
		}
	}
	return ctx.Err()
}

func sleepDeliveryRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

type permanentDeliveryError struct{ error }

func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanentDeliveryError{err}
}

func IsPermanent(err error) bool {
	var target permanentDeliveryError
	return errors.As(err, &target)
}

func (c *Consumer) applyDelivery(ctx context.Context, delivery *jetstream.Delivery) error {
	if delivery == nil {
		return Permanent(errors.New("storage event delivery is empty"))
	}
	if delivery.DecodeError != nil {
		return Permanent(delivery.DecodeError)
	}
	message, payload, err := events.DecodeRaw(
		c.registry,
		delivery.RawData,
		delivery.Subject,
		delivery.RawMessageID,
		delivery.ContentType,
	)
	if err != nil {
		return Permanent(err)
	}
	switch value := payload.(type) {
	case *storagepb.DatasetRowsUpserted:
		return c.handler.HandleDatasetRows(ctx, message, value)
	case *storagepb.DatasetPeriodCollected:
		handler, ok := c.handler.(DatasetPeriodCollectedHandler)
		if !ok {
			return Permanent(errors.New("storage view dataset-period handler is unavailable"))
		}
		return handler.HandleDatasetPeriodCollected(ctx, message, value)
	case *storagepb.FactorPeriodComputed:
		handler, ok := c.handler.(FactorPeriodComputedHandler)
		if !ok {
			return Permanent(errors.New("storage view factor-period handler is unavailable"))
		}
		return handler.HandleFactorPeriodComputed(ctx, message, value)
	case *storagepb.DatasetSyncPoint:
		handler, ok := c.handler.(DatasetSyncPointHandler)
		if !ok {
			return Permanent(errors.New("storage view sync-point handler is unavailable"))
		}
		return handler.HandleDatasetSyncPoint(ctx, message, value)
	default:
		return Permanent(fmt.Errorf("unsupported storage view event payload %T", payload))
	}
}
