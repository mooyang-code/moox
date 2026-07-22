package eventconsumer

import (
	"context"
	"errors"
	"fmt"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	eventstoragepb "github.com/mooyang-code/moox/packages/events/storagepb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"google.golang.org/protobuf/proto"
)

// DatasetPublisher publishes one durable message to the subject owned by a
// Dataset. The transport message id is stable for a DataNode outbox id.
type DatasetPublisher struct {
	client *jetstream.Client
}

func (p *DatasetPublisher) PublishMessage(ctx context.Context, data []byte) error {
	_, err := p.PublishMessageWithAck(ctx, data)
	return err
}

func (p *DatasetPublisher) PublishMessageWithAck(ctx context.Context, data []byte) (*jetstream.PublishAck, error) {
	if p == nil || p.client == nil {
		return nil, errors.New("storage eventbus client is nil")
	}
	msg := &eventpb.EventMessage{}
	if err := proto.Unmarshal(data, msg); err != nil {
		return nil, err
	}
	if msg.GetEventName() != events.StorageRowsUpserted.Name || msg.GetEventVersion() != events.StorageRowsUpserted.Version || msg.GetEventId() == "" || msg.GetSubjectId() == "" || msg.GetSpaceId() == "" {
		return nil, errors.New("dataset event envelope is incomplete")
	}
	subject := eventSubject(msg)
	if subject == "" {
		return nil, errors.New("dataset event subject cannot be derived")
	}
	return p.client.PublishRaw(ctx, subject, msg.GetEventId(), data, events.ContentType)
}

func eventSubject(message *eventpb.EventMessage) string {
	registry, err := events.DefaultRegistry()
	if err != nil {
		return ""
	}
	spec, ok := registry.Spec(events.StorageRowsUpserted)
	if !ok {
		return ""
	}
	template, err := events.NewSubjectTemplate(spec.Subject)
	if err != nil {
		return ""
	}
	subject, err := template.Render(message.GetSpaceId(), message.GetSubjectId())
	if err != nil {
		return ""
	}
	return subject
}

func NewDatasetPublisher(client *jetstream.Client, _ string) *DatasetPublisher {
	return &DatasetPublisher{client: client}
}

func (p *DatasetPublisher) Publish(ctx context.Context, event *pb.RowsUpserted, outboxID uint64) error {
	if p == nil || p.client == nil {
		return errors.New("storage eventbus client is nil")
	}
	if event == nil || event.GetSpaceId() == "" || event.GetDatasetId() == "" {
		return errors.New("rows upserted payload requires space_id and dataset_id")
	}
	rowPayload, err := proto.MarshalOptions{Deterministic: true}.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal rows payload: %w", err)
	}
	messageID := fmt.Sprintf("storage-%d", outboxID)
	if outboxID == 0 {
		messageID = fmt.Sprintf("storage-%s", event.GetDatasetId())
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return err
	}
	encoded, err := registry.Encode(events.StorageRowsUpserted, &eventstoragepb.RowsUpserted{DatasetId: event.GetDatasetId(), Rows: rowPayload}, events.PublishOptions{EventID: messageID, OccurredAt: time.Now().UTC(), SpaceID: event.GetSpaceId(), SubjectID: event.GetDatasetId()})
	if err != nil {
		return err
	}
	body, err := proto.MarshalOptions{Deterministic: true}.Marshal(encoded.Message)
	if err != nil {
		return err
	}
	_, err = p.client.PublishRaw(ctx, encoded.Subject, messageID, body, events.ContentType)
	return err
}
