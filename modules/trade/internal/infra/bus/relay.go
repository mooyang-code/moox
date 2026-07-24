package bus

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
)

type Publisher interface {
	PublishMessage(context.Context, *eventpb.EventMessage) (*jetstream.PublishAck, error)
}

type Relay struct {
	Store              *store.Store
	Publisher          Publisher
	InstanceID, BootID string
}

func (r Relay) RunOnce(ctx context.Context, limit int) error {
	if r.Store == nil || r.Publisher == nil {
		return fmt.Errorf("trade outbox relay dependencies are required")
	}
	rows, err := r.Store.ClaimOutbox(ctx, limit, 30*time.Second)
	if err != nil {
		return err
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return err
	}
	for _, row := range rows {
		message, err := registry.UnmarshalMessage(row.EventData)
		if err != nil {
			_ = r.Store.ReleaseOutbox(ctx, row.ID, err.Error())
			return err
		}
		// Validate that the durable row identity and the envelope identity stay
		// aligned before handing bytes to the transport publisher.
		if message.GetEventId() != row.MessageID {
			err = fmt.Errorf("trade outbox event_id %q does not match row %q", message.GetEventId(), row.MessageID)
			_ = r.Store.ReleaseOutbox(ctx, row.ID, err.Error())
			return err
		}
		if _, err = r.Publisher.PublishMessage(ctx, message); err != nil {
			_ = r.Store.ReleaseOutbox(ctx, row.ID, err.Error())
			return err
		}
		if err = r.Store.MarkOutboxPublished(ctx, row.ID); err != nil {
			return err
		}
	}
	return nil
}
