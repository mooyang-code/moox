package view

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
)

// storageViewDLQPublisher bridges the governed Storage event into the
// existing EventBus rejection stream. DLQ is an operational quarantine
// contract, not a second Storage business event; the original bytes and
// routing identity remain available for diagnosis/replay.
func storageViewDLQPublisher(client *jetstream.Client) func(context.Context, *jetstream.Delivery, error) error {
	return func(ctx context.Context, delivery *jetstream.Delivery, reason error) error {
		if client == nil {
			return fmt.Errorf("storage view DLQ client is nil")
		}
		message, err := buildStorageViewDLQMessage(delivery, reason)
		if err != nil {
			return err
		}
		registry, err := events.DefaultRegistry()
		if err != nil {
			return err
		}
		publisher, err := events.NewPublisher(client, registry)
		if err != nil {
			return err
		}
		_, err = publisher.PublishMessage(ctx, message)
		return err
	}
}

func buildStorageViewDLQMessage(delivery *jetstream.Delivery, reason error) (*eventpb.EventMessage, error) {
	if delivery == nil {
		return nil, fmt.Errorf("storage view DLQ delivery is nil")
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	return events.RejectedMessage(registry, delivery, boundedReason(reason), "moox-storage-view", "storage-view")
}

func boundedReason(reason error) string {
	if reason == nil {
		return "storage view retry exhausted"
	}
	value := reason.Error()
	if len(value) > 1024 {
		return value[:1024]
	}
	return value
}
