package view

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	"github.com/mooyang-code/moox/packages/jetstream"
)

func (s *Service) processDelivery(ctx context.Context, delivery *jetstream.Delivery, queued ...*deliveryHeartbeat) error {
	return s.processDeliveryWithPolicy(ctx, delivery, firstHeartbeat(queued), defaultMaxRetryAttempts)
}

func firstHeartbeat(queued []*deliveryHeartbeat) *deliveryHeartbeat {
	if len(queued) == 0 {
		return nil
	}
	return queued[0]
}

func (s *Service) processDeliveryWithPolicy(ctx context.Context, delivery *jetstream.Delivery, heartbeat *deliveryHeartbeat, maxRetryAttempts int) error {
	return s.processDeliveryWithApply(ctx, delivery, heartbeat, maxRetryAttempts, func(ctx context.Context, delivery *jetstream.Delivery) error {
		return s.applyDelivery(ctx, delivery)
	})
}

func (s *Service) processDeliveryWithApply(ctx context.Context, delivery *jetstream.Delivery, heartbeat *deliveryHeartbeat, maxRetryAttempts int, apply func(context.Context, *jetstream.Delivery) error) error {
	return s.processDeliveryWithApplyAndActions(ctx, delivery, heartbeat, maxRetryAttempts, apply, deliveryActions{
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

func (s *Service) processDeliveryWithApplyAndActions(ctx context.Context, delivery *jetstream.Delivery, heartbeat *deliveryHeartbeat, maxRetryAttempts int, apply func(context.Context, *jetstream.Delivery) error, actions deliveryActions) error {
	if s == nil {
		return errors.New("storage view service is nil")
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
	metrics := s.metrics
	if metrics == nil {
		metrics = observability.DefaultViewMetrics
	}
	defer func() { metrics.ObserveDeliveryDuration(time.Since(started)) }()
	if delivery != nil && delivery.DeliveryCount > 1 {
		metrics.IncRedelivery()
	}
	s.liveWork.Add(1)
	defer s.liveWork.Add(-1)
	if maxRetryAttempts < 1 {
		maxRetryAttempts = defaultMaxRetryAttempts
	}
	if heartbeat == nil {
		heartbeat = newDeliveryHeartbeat(ctx, delivery, deliveryHeartbeatInterval(120*time.Second), metrics)
	}
	defer func() { heartbeat.stop() }()
	if err := s.acquireLiveDelivery(ctx, delivery); err != nil {
		return errors.Join(err, heartbeat.err())
	}
	defer s.releaseLiveDelivery()
	retryCount := 0
	for ctx.Err() == nil {
		err := apply(ctx, delivery)
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
		if isPermanentDeliveryError(err) {
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
		if retryCount >= maxRetryAttempts {
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

func isPermanentDeliveryError(err error) bool {
	var target permanentDeliveryError
	return errors.As(err, &target)
}
