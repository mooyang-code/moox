package eventconsumer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/trigger"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	storagepb "github.com/mooyang-code/moox/packages/storagepb"
)

type storageEventHandler struct {
	eventBatcher *trigger.EventBatcher
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
	_, payload, err := events.DecodeDatasetRowsUpsertedWithContentType(
		registry,
		delivery.RawData,
		delivery.Subject,
		delivery.RawMessageID,
		delivery.ContentType,
	)
	if err != nil {
		return h.reject(ctx, delivery, err)
	}
	if payload.GetSpaceId() == "" || payload.GetDatasetId() == "" {
		return h.reject(ctx, delivery, fmt.Errorf("storage event payload identity is incomplete"))
	}
	return h.ingest(ctx, delivery, payload)
}

func (h storageEventHandler) reject(_ context.Context, _ *jetstream.Delivery, reason error) jetstream.HandlerResult {
	return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("factor event rejected: %w", reason)}
}

func (h storageEventHandler) ingest(ctx context.Context, delivery *jetstream.Delivery, event *storagepb.DatasetRowsUpserted) jetstream.HandlerResult {
	if h.eventBatcher == nil {
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: errors.New("factor event batcher is unavailable")}
	}
	if err := h.eventBatcher.IngestMessage(ctx, delivery.RawMessageID, event, time.Now().UTC()); err != nil {
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: err}
	}
	return jetstream.HandlerResult{Decision: jetstream.ACK}
}
