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

type DeliveryHandlerFunc func(context.Context, *Delivery) HandlerResult

func (f DeliveryHandlerFunc) Handle(ctx context.Context, delivery *Delivery) HandlerResult {
	return f(ctx, delivery)
}

// ConsumerAPI 是 Runner 所需的最小消费接口。
type ConsumerAPI interface {
	Fetch(context.Context, int) ([]*Delivery, error)
	Close() error
}

// ErrorReporter observes fetch and in-progress transport failures. Handler
// business errors belong to the handler, while delivery actions are observed
// through ActionReporter.
type ErrorReporter interface {
	Report(error)
}

type ErrorReporterFunc func(error)

func (f ErrorReporterFunc) Report(err error) {
	if f != nil && err != nil {
		f(err)
	}
}

// ActionReporter observes the actual ACK/NAK/TERM attempt for a delivery.
// Reporting is best-effort and cannot change transport behavior. Reports may
// be concurrent when RunnerConfig.IndependentBatch is enabled.
type ActionReporter interface {
	ReportAction(context.Context, *Delivery, HandlerResult, error)
}

type RunnerConfig struct {
	BatchSize          int
	InProgressInterval time.Duration
	ErrorReporter      ErrorReporter
	ActionReporter     ActionReporter
	IndependentBatch   bool
}

type Runner struct {
	consumer ConsumerAPI
	handler  DeliveryHandler
	cfg      RunnerConfig
}

// ApplyHandlerResult performs the transport action selected by a handler.
// Keeping this separate lets direct callers use the same ACK/NAK/TERM
// contract as Runner without duplicating the action switch.
func ApplyHandlerResult(ctx context.Context, delivery *Delivery, result HandlerResult) error {
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	if delivery == nil {
		return ErrInvalidDelivery
	}
	switch result.Decision {
	case ACK:
		return delivery.Ack(ctx)
	case RETRY:
		return delivery.Nak(ctx, result.Delay)
	case TERM:
		return delivery.Term(ctx)
	default:
		return fmt.Errorf("invalid handler decision %d", result.Decision)
	}
}

func NewRunner(consumer ConsumerAPI, handler DeliveryHandler, cfg RunnerConfig) *Runner {
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
		if r.cfg.IndependentBatch {
			batchErr = r.handleIndependentBatch(ctx, deliveries)
		} else {
			batchErr = r.handleSequentialBatch(ctx, deliveries)
		}
		if ctx.Err() != nil {
			return nil
		}
		if isNormalStop(ctx, fetchErr) {
			return nil
		}
		if fetchErr != nil && !isDecodeOnly(fetchErr) && !errors.Is(fetchErr, nats.ErrTimeout) {
			wrappedFetchErr := fmt.Errorf("fetch deliveries: %w", fetchErr)
			r.report(wrappedFetchErr)
			batchErr = errors.Join(batchErr, wrappedFetchErr)
		}
		if batchErr != nil {
			return batchErr
		}
	}
}

func (r *Runner) handleSequentialBatch(ctx context.Context, deliveries []*Delivery) error {
	var batchErr error
	for index, delivery := range deliveries {
		if ctx.Err() != nil {
			return batchErr
		}
		result, inProgressErr, actionErr := r.handle(ctx, delivery)
		if inProgressErr != nil {
			r.report(inProgressErr)
			batchErr = errors.Join(batchErr, inProgressErr)
		}
		if actionErr != nil {
			batchErr = errors.Join(batchErr, actionErr)
		}
		if result.Decision == RETRY {
			// Keep this fetched batch together. A later fetch may still overtake
			// these delayed deliveries, so ordered domains must reject stale work.
			for _, pending := range deliveries[index+1:] {
				pendingResult := HandlerResult{Decision: RETRY, Delay: result.Delay}
				pendingActionErr := ApplyHandlerResult(ctx, pending, pendingResult)
				r.reportAction(ctx, pending, pendingResult, pendingActionErr)
				if pendingActionErr != nil {
					batchErr = errors.Join(batchErr, fmt.Errorf("apply pending handler result: %w", pendingActionErr))
				}
			}
			break
		}
	}
	return batchErr
}

func (r *Runner) handleIndependentBatch(ctx context.Context, deliveries []*Delivery) error {
	perDeliveryErrs := make([]error, len(deliveries))
	inProgressErrs := make([]error, len(deliveries))
	handlers := make([]func() error, len(deliveries))
	for index, delivery := range deliveries {
		index, delivery := index, delivery
		handlers[index] = func() error {
			_, inProgressErr, actionErr := r.handle(ctx, delivery)
			inProgressErrs[index] = inProgressErr
			perDeliveryErrs[index] = errors.Join(inProgressErr, actionErr)
			return nil
		}
	}

	waitErr := trpc.GoAndWait(handlers...)
	var batchErr error
	for index, deliveryErr := range perDeliveryErrs {
		if inProgressErrs[index] != nil {
			r.report(inProgressErrs[index])
		}
		if deliveryErr != nil {
			batchErr = errors.Join(batchErr, fmt.Errorf("delivery %d: %w", index, deliveryErr))
		}
	}
	if waitErr != nil {
		batchErr = errors.Join(batchErr, fmt.Errorf("process independent batch: %w", waitErr))
	}
	return batchErr
}

func (r *Runner) handle(ctx context.Context, delivery *Delivery) (HandlerResult, error, error) {
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

	var inProgressErr error
	for {
		select {
		case err := <-heartbeatErrs:
			inProgressErr = errors.Join(inProgressErr, err)
		default:
			goto heartbeatDone
		}
	}

heartbeatDone:
	if ctx.Err() != nil {
		return result, inProgressErr, nil
	}
	if delivery == nil {
		err := ErrInvalidDelivery
		r.reportAction(ctx, delivery, result, err)
		return result, inProgressErr, err
	}
	actionErr := ApplyHandlerResult(ctx, delivery, result)
	r.reportAction(ctx, delivery, result, actionErr)
	if actionErr != nil {
		actionErr = fmt.Errorf("apply handler result: %w", actionErr)
	}
	return result, inProgressErr, actionErr
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

func (r *Runner) reportAction(
	ctx context.Context,
	delivery *Delivery,
	result HandlerResult,
	err error,
) {
	if r.cfg.ActionReporter != nil {
		func() {
			defer func() {
				// The transport action has already happened; an observer cannot
				// be allowed to change delivery control flow.
				_ = recover()
			}()
			r.cfg.ActionReporter.ReportAction(ctx, delivery, result, err)
		}()
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
