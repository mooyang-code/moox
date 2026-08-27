package eventconsumer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/trigger"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"trpc.group/trpc-go/trpc-go/log"
)

type storageEventHandler struct {
	executor             ViewReadyExecutor
	executionTimeout     time.Duration
	stallThreshold       time.Duration
	maxExecutionAttempts int
	progress             *progressState
}

func (h storageEventHandler) Handle(ctx context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
	if delivery == nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: jetstream.ErrInvalidDelivery}
	}
	if delivery.ContentType != events.ContentType {
		return h.reject(ctx, delivery, fmt.Errorf("unexpected storage event content type %q", delivery.ContentType))
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: err}
	}
	if h.executor == nil {
		return h.reject(ctx, delivery, fmt.Errorf("factor View-ready executor is unavailable"))
	}
	message, payload, err := events.DecodeViewSourcePeriodReadyWithContentType(
		registry,
		delivery.RawData,
		delivery.Subject,
		delivery.RawMessageID,
		delivery.ContentType,
	)
	if err != nil {
		return h.reject(ctx, delivery, err)
	}
	if message.GetSpaceId() == "" || message.GetEventId() == "" || payload.GetSourceViewId() == "" {
		return h.reject(ctx, delivery, fmt.Errorf("storage event payload identity is incomplete"))
	}
	log.InfoContextf(ctx, "factor ViewSourcePeriodReady received event_id=%s space_id=%s view_id=%s period=%d", message.GetEventId(), message.GetSpaceId(), payload.GetSourceViewId(), payload.GetPeriodTime())
	executionTimeout, stallThreshold := h.executionTimeout, h.stallThreshold
	if budgeter, ok := h.executor.(executionBudgeter); ok {
		budgetLookupTimeout := 30 * time.Second
		if executionTimeout > 0 && executionTimeout < budgetLookupTimeout {
			budgetLookupTimeout = executionTimeout
		}
		budgetCtx, budgetCancel := context.WithTimeout(ctx, budgetLookupTimeout)
		budget, budgetErr := budgeter.ExecutionBudget(budgetCtx, message.GetSpaceId(), payload)
		budgetCancel()
		if budgetErr != nil {
			return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: fmt.Errorf("calculate factor execution budget: %w", budgetErr)}
		}
		if budget > executionTimeout {
			executionTimeout = budget
		}
	}
	h.progress.beginWithThreshold(message.GetEventId(), time.Now(), stallThreshold)
	executionCtx := ctx
	cancel := func() {}
	if executionTimeout > 0 {
		executionCtx, cancel = context.WithTimeout(ctx, executionTimeout)
	}
	err = h.executor.Execute(executionCtx, message.GetSpaceId(), message.GetEventId(), payload)
	cancel()
	if err != nil {
		log.ErrorContextf(ctx, "factor ViewSourcePeriodReady execution failed event_id=%s space_id=%s view_id=%s period=%d: %v", message.GetEventId(), message.GetSpaceId(), payload.GetSourceViewId(), payload.GetPeriodTime(), err)
		if errors.Is(err, trigger.ErrNoExecutableBinding) {
			// A Source View can legitimately outlive its last binding. Do not
			// block the durable lane on a period that no longer has work.
			h.progress.finish(message.GetEventId(), true, nil, time.Now())
			return jetstream.HandlerResult{Decision: jetstream.ACK}
		}
		h.progress.finish(message.GetEventId(), false, err, time.Now())
		if errors.Is(err, context.DeadlineExceeded) {
			timeoutAttempts := h.progress.recordExecutionTimeout(message.GetEventId())
			if h.maxExecutionAttempts > 0 && timeoutAttempts >= h.maxExecutionAttempts {
				// Keep the event durable, but delay its next attempt so newer
				// periods can continue. Stale work is idempotently rejected by
				// the executor when this event is delivered again.
				return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: 30 * time.Second, Err: fmt.Errorf("factor event retry threshold reached after %d execution timeouts: %w", timeoutAttempts, err)}
			}
		}
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: err}
	}
	h.progress.finish(message.GetEventId(), true, nil, time.Now())
	return jetstream.HandlerResult{Decision: jetstream.ACK}
}

func (h storageEventHandler) reject(_ context.Context, _ *jetstream.Delivery, reason error) jetstream.HandlerResult {
	// Malformed or unsupported messages cannot become valid through redelivery.
	// Terminate them so one poison event cannot block all later factor periods.
	return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("factor event rejected: %w", reason)}
}
