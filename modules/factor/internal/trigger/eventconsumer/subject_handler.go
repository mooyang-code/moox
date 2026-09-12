package eventconsumer

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/trigger"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
)

type SubjectSubmitter interface {
	Submit(context.Context, trigger.SubjectEvent) error
}

type SubjectHandler struct {
	registry *events.Registry
	batcher  SubjectSubmitter
}

func NewSubjectHandler(batcher SubjectSubmitter) (*SubjectHandler, error) {
	if batcher == nil {
		return nil, fmt.Errorf("subject batcher is required")
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	return &SubjectHandler{registry: registry, batcher: batcher}, nil
}

func (h *SubjectHandler) Handle(ctx context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
	if delivery == nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: jetstream.ErrInvalidDelivery}
	}
	message, ready, err := events.DecodeViewSourceSubjectReadyWithContentType(h.registry, delivery.RawData, delivery.Subject, delivery.RawMessageID, delivery.ContentType)
	if err != nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("factor subject event rejected: %w", err)}
	}
	if err := h.batcher.Submit(ctx, trigger.SubjectEvent{SpaceID: message.GetSpaceId(), EventID: message.GetEventId(), Ready: ready}); err != nil {
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: err}
	}
	return jetstream.HandlerResult{Decision: jetstream.ACK}
}
