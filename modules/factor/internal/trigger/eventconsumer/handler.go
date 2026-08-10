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
	executor ViewReadyExecutor
}

func (h storageEventHandler) Handle(ctx context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
	if delivery == nil {
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: jetstream.ErrInvalidDelivery}
	}
	if delivery.ContentType != events.ContentType {
		return h.reject(ctx, delivery, fmt.Errorf("unexpected storage event content type %q", delivery.ContentType))
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: err}
	}
	if h.executor == nil {
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: fmt.Errorf("factor View-ready executor is unavailable")}
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
	if err := h.executor.Execute(ctx, message.GetSpaceId(), message.GetEventId(), payload); err != nil {
		log.ErrorContextf(ctx, "factor ViewSourcePeriodReady execution failed event_id=%s space_id=%s view_id=%s period=%d: %v", message.GetEventId(), message.GetSpaceId(), payload.GetSourceViewId(), payload.GetPeriodTime(), err)
		if errors.Is(err, trigger.ErrNoExecutableBinding) {
			// A Source View can legitimately outlive its last binding. Do not
			// block the durable lane on a period that no longer has work.
			return jetstream.HandlerResult{Decision: jetstream.ACK}
		}
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: err}
	}
	return jetstream.HandlerResult{Decision: jetstream.ACK}
}

func (h storageEventHandler) reject(_ context.Context, _ *jetstream.Delivery, reason error) jetstream.HandlerResult {
	// Keep the durable lane pending for malformed/unsupported input. An
	// operator can repair or explicitly skip the message; silently TERM'ing it
	// would let later periods overtake an event whose result is unknown.
	return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: fmt.Errorf("factor event rejected: %w", reason)}
}
