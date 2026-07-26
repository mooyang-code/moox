package outbox

import (
	"context"
	"errors"
	"fmt"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"google.golang.org/protobuf/proto"
)

// JetStreamPublisher publishes one durable message to the subject owned by a
// Dataset. The transport message id is stable for a DataNode outbox id.
type JetStreamPublisher struct {
	publisher *events.Publisher
}

func (p *JetStreamPublisher) PublishMessage(ctx context.Context, data []byte) error {
	_, err := p.PublishMessageWithAck(ctx, data)
	return err
}

func (p *JetStreamPublisher) PublishMessageWithAck(ctx context.Context, data []byte) (*jetstream.PublishAck, error) {
	if p == nil || p.publisher == nil {
		return nil, errors.New("storage eventbus client is nil")
	}
	_, _, err := validateDatasetEvent(data)
	if err != nil {
		return nil, err
	}
	message := new(eventpb.EventMessage)
	if err := proto.Unmarshal(data, message); err != nil {
		return nil, err
	}
	return p.publisher.PublishMessage(ctx, message)
}

func validateDatasetEvent(data []byte) (string, string, error) {
	message := &eventpb.EventMessage{}
	if err := proto.Unmarshal(data, message); err != nil {
		return "", "", err
	}
	if message.GetEventName() != events.DatasetRowsUpserted.Name() || message.GetEventVersion() != events.DatasetRowsUpserted.Version() || message.GetEventId() == "" || message.GetSubjectId() == "" || message.GetSpaceId() == "" {
		return "", "", errors.New("dataset event envelope is incomplete")
	}
	if message.GetEventId() == "outbox-pending" {
		return "", "", errors.New("dataset event cannot use placeholder event id")
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return "", "", err
	}
	subject, err := registry.RenderSubject(events.DatasetRowsUpserted, message.GetSpaceId(), message.GetSubjectId())
	if err != nil {
		return "", "", err
	}
	if _, _, err := events.DecodeDatasetRowsUpserted(registry, data, subject, message.GetEventId()); err != nil {
		return "", "", fmt.Errorf("validate dataset event: %w", err)
	}
	return subject, message.GetEventId(), nil
}

func NewJetStreamPublisher(client *jetstream.Client) *JetStreamPublisher {
	if client == nil {
		return &JetStreamPublisher{}
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return &JetStreamPublisher{}
	}
	publisher, err := events.NewPublisher(client, registry)
	if err != nil {
		return &JetStreamPublisher{}
	}
	return &JetStreamPublisher{publisher: publisher}
}
