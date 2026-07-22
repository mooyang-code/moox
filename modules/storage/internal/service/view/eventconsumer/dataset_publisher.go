package eventconsumer

import (
	"context"
	"errors"
	"fmt"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/messagepb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// DatasetPublisher publishes one durable message to the subject owned by a
// Dataset. The transport message id is stable for a DataNode outbox id.
type DatasetPublisher struct {
	client   *jetstream.Client
	producer *messagepb.Producer
}

func (p *DatasetPublisher) PublishMessage(ctx context.Context, data []byte) error {
	if p == nil || p.client == nil {
		return errors.New("storage eventbus client is nil")
	}
	msg := &messagepb.MooxMessage{}
	if err := proto.Unmarshal(data, msg); err != nil {
		return err
	}
	if msg.GetTopic() == "" || msg.GetMessageId() == "" {
		return errors.New("dataset event envelope is incomplete")
	}
	_, err := p.client.Publish(ctx, msg)
	return err
}

func NewDatasetPublisher(client *jetstream.Client, producerID string) *DatasetPublisher {
	return &DatasetPublisher{client: client, producer: &messagepb.Producer{ServiceName: "moox-storage", InstanceId: producerID}}
}

func (p *DatasetPublisher) Publish(ctx context.Context, event *pb.DatasetFieldsChanged, outboxID uint64) error {
	if p == nil || p.client == nil {
		return errors.New("storage eventbus client is nil")
	}
	if event == nil || event.GetSpaceId() == "" || event.GetDatasetId() == "" {
		return errors.New("dataset fields changed payload requires space_id and dataset_id")
	}
	subject, err := DatasetFieldsChangedSubject("", event.GetSpaceId(), event.GetDatasetId())
	if err != nil {
		return err
	}
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal dataset fields changed: %w", err)
	}
	messageID := fmt.Sprintf("storage-%d", outboxID)
	if outboxID == 0 {
		messageID = fmt.Sprintf("storage-%s", event.GetDatasetId())
	}
	now := timestamppb.Now()
	msg := &messagepb.MooxMessage{
		ProtocolVersion: jetstream.ProtocolVersion,
		MessageId:       messageID,
		Topic:           subject,
		Kind:            messagepb.MessageKind_MESSAGE_KIND_EVENT,
		Producer:        p.producer,
		SpaceId:         event.GetSpaceId(),
		OccurredAt:      now,
		PublishedAt:     now,
		ContentType:     jetstream.StorageFieldsChangedContentType,
		MessageType:     jetstream.StorageFieldsChangedMessageType,
		Payload:         payload,
		Attributes:      map[string]string{"dataset_id": event.GetDatasetId()},
	}
	_, err = p.client.Publish(ctx, msg)
	return err
}
