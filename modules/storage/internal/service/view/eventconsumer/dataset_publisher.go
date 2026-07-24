package eventconsumer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/eventcontract"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"google.golang.org/protobuf/proto"
)

// DatasetPublisher publishes one durable message to the subject owned by a
// Dataset. The transport message id is stable for a DataNode outbox id.
type DatasetPublisher struct {
	publisher *events.Publisher
}

func (p *DatasetPublisher) PublishMessage(ctx context.Context, data []byte) error {
	_, err := p.PublishMessageWithAck(ctx, data)
	return err
}

func (p *DatasetPublisher) PublishMessageWithAck(ctx context.Context, data []byte) (*jetstream.PublishAck, error) {
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

func NewDatasetPublisher(client *jetstream.Client, _ string) *DatasetPublisher {
	if client == nil {
		return &DatasetPublisher{}
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return &DatasetPublisher{}
	}
	publisher, err := events.NewPublisher(client, registry)
	if err != nil {
		return &DatasetPublisher{}
	}
	return &DatasetPublisher{publisher: publisher}
}

func (p *DatasetPublisher) Publish(ctx context.Context, event *pb.RowsUpserted, outboxID uint64) error {
	if p == nil || p.publisher == nil {
		return errors.New("storage eventbus client is nil")
	}
	if event == nil || event.GetSpaceId() == "" || event.GetDatasetId() == "" {
		return errors.New("rows upserted payload requires space_id and dataset_id")
	}
	rowPayload, err := eventcontract.ToSharedRows(event)
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
	encoded, err := registry.Encode(events.DatasetRowsUpserted, rowPayload, events.PublishOptions{EventID: messageID, OccurredAt: time.Now().UTC(), SpaceID: event.GetSpaceId(), SubjectID: event.GetDatasetId()})
	if err != nil {
		return err
	}
	_, err = p.publisher.PublishMessage(ctx, encoded.Message)
	return err
}
