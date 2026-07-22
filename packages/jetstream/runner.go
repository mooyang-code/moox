package jetstream

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	trpc "trpc.group/trpc-go/trpc-go"
)

// HandlerDecision is the transport outcome selected by a delivery handler.
type HandlerDecision uint8

const (
	ACK HandlerDecision = iota + 1
	RETRY
	TERM

	HandlerDecisionACK   = ACK
	HandlerDecisionRETRY = RETRY
	HandlerDecisionTERM  = TERM
	HandlerDecisionRetry = RETRY
	HandlerDecisionTerm  = TERM
)

// HandlerResult keeps domain processing separate from JetStream actions.
type HandlerResult struct {
	Decision HandlerDecision
	Delay    time.Duration
	Err      error
}

// DeliveryHandler decides what should happen to one delivery. It must not
// call Ack, Nak, InProgress, or Term itself.
type DeliveryHandler interface {
	Handle(context.Context, *Delivery) HandlerResult
}

// PullConsumerAPI is the fetch/close surface required by Runner. PullConsumer
// implements it, while small fakes can be used by module tests.
type PullConsumerAPI interface {
	Fetch(context.Context, int) ([]*Delivery, error)
	Close() error
}

// ErrorReporter receives handler, fetch, and transport-action failures while
// Runner also returns them to its caller.
type ErrorReporter interface {
	Report(error)
}

type ErrorReporterFunc func(error)

func (f ErrorReporterFunc) Report(err error) {
	if f != nil && err != nil {
		f(err)
	}
}

type RunnerConfig struct {
	BatchSize          int
	InProgressInterval time.Duration
	ErrorReporter      ErrorReporter
}

type Runner struct {
	consumer PullConsumerAPI
	handler  DeliveryHandler
	cfg      RunnerConfig
}

func NewRunner(consumer PullConsumerAPI, handler DeliveryHandler, cfg RunnerConfig) *Runner {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 1
	}
	return &Runner{consumer: consumer, handler: handler, cfg: cfg}
}

func (r *Runner) Run(ctx context.Context) error {
	if r == nil || r.consumer == nil {
		return ErrInvalidConsumer
	}
	if r.handler == nil {
		return errors.New("jetstream runner handler is nil")
	}
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	for {
		if ctx.Err() != nil {
			return nil
		}
		deliveries, fetchErr := r.consumer.Fetch(ctx, r.cfg.BatchSize)
		if len(deliveries) == 0 {
			if isNormalStop(ctx, fetchErr) {
				return nil
			}
			if errors.Is(fetchErr, nats.ErrTimeout) || isDecodeOnly(fetchErr) {
				continue
			}
			if fetchErr != nil {
				r.report(fetchErr)
				return fmt.Errorf("fetch deliveries: %w", fetchErr)
			}
			continue
		}

		var batchErr error
		for _, delivery := range deliveries {
			if ctx.Err() != nil {
				return nil
			}
			if err := r.handle(ctx, delivery); err != nil {
				batchErr = errors.Join(batchErr, err)
			}
		}
		if ctx.Err() != nil {
			return nil
		}
		if isNormalStop(ctx, fetchErr) {
			return nil
		}
		if fetchErr != nil && !isDecodeOnly(fetchErr) && !errors.Is(fetchErr, nats.ErrTimeout) {
			batchErr = errors.Join(batchErr, fmt.Errorf("fetch deliveries: %w", fetchErr))
		}
		if batchErr != nil {
			r.report(batchErr)
			return batchErr
		}
	}
}

func (r *Runner) handle(ctx context.Context, delivery *Delivery) error {
	stopHeartbeat := make(chan struct{})
	heartbeatErrs := make(chan error, 1)
	var heartbeatWG sync.WaitGroup
	if r.cfg.InProgressInterval > 0 && delivery != nil {
		heartbeatWG.Add(1)
		go r.heartbeat(ctx, delivery, stopHeartbeat, heartbeatErrs, &heartbeatWG)
	}
	result := r.handler.Handle(ctx, delivery)
	close(stopHeartbeat)
	heartbeatWG.Wait()

	var allErr error
	if result.Err != nil {
		r.report(result.Err)
		allErr = errors.Join(allErr, result.Err)
	}
	for {
		select {
		case err := <-heartbeatErrs:
			r.report(err)
			allErr = errors.Join(allErr, err)
		default:
			goto heartbeatDone
		}
	}

heartbeatDone:
	if ctx.Err() != nil {
		return nil
	}
	if delivery == nil {
		err := ErrInvalidDelivery
		r.report(err)
		return errors.Join(allErr, err)
	}
	var actionErr error
	switch result.Decision {
	case ACK:
		actionErr = delivery.Ack(ctx)
	case RETRY:
		actionErr = delivery.Nak(ctx, result.Delay)
	case TERM:
		actionErr = delivery.Term(ctx)
	default:
		actionErr = fmt.Errorf("invalid handler decision %d", result.Decision)
	}
	if actionErr != nil {
		r.report(actionErr)
		allErr = errors.Join(allErr, actionErr)
	}
	return allErr
}

func (r *Runner) heartbeat(ctx context.Context, delivery *Delivery, stop <-chan struct{}, errs chan<- error, wg *sync.WaitGroup) {
	defer wg.Done()
	ticker := time.NewTicker(r.cfg.InProgressInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := delivery.InProgress(ctx); err != nil && ctx.Err() == nil {
				select {
				case errs <- fmt.Errorf("in-progress delivery: %w", err):
				default:
				}
			}
		}
	}
}

func (r *Runner) report(err error) {
	if r.cfg.ErrorReporter != nil && err != nil {
		r.cfg.ErrorReporter.Report(err)
	}
}

func isNormalStop(ctx context.Context, err error) bool {
	if ctx != nil && ctx.Err() != nil {
		return true
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrClosed)
}

func isDecodeOnly(err error) bool {
	if err == nil || !errors.Is(err, ErrDecode) {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, child := range joined.Unwrap() {
			if !isDecodeOnly(child) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return isDecodeOnly(wrapped.Unwrap())
	}
	return true
}
